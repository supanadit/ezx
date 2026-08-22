// Package orchestrator implements the supervisor use-case: it walks a
// ProcessChain tree and drives each node through its lifecycle (provision →
// probe-gated start → supervise → graceful drain) by composing the process and
// logger Ports with the internal/repository helpers. It contains no OS
// specifics and imports no adapter types.
package orchestrator

import (
	"context"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/internal/repository"
	"github.com/supanadit/ezx/logger"
	"github.com/supanadit/ezx/process"
)

// ProcessFactory constructs a ProcessRepository handle for a ProcessNode. It is
// injected so the orchestrator never imports a concrete driver.
type ProcessFactory func(node domain.ProcessNode) process.ProcessRepository

// Service is the supervisor. It composes the module Ports to run a ProcessChain.
type Service struct {
	proc   ProcessFactory
	log    logger.Logger
	health domain.HealthService

	mu        sync.Mutex
	active    int
	triggers  map[string]*domain.Trigger
	schedules map[string]*repository.Cron
}

// NewService builds a supervisor from the injected Ports. health is optional
// (nil means health/readiness is not surfaced to the delivery layer).
func NewService(
	proc ProcessFactory,
	log logger.Logger,
	health domain.HealthService,
) *Service {
	return &Service{
		proc:      proc,
		log:       log,
		health:    health,
		triggers:  make(map[string]*domain.Trigger),
		schedules: make(map[string]*repository.Cron),
	}
}

// Run executes every root in the chain and supervises the full dependency tree.
func (s *Service) Run(ctx context.Context, chain domain.ProcessChain) error {
	// A lone leaf root with no restart policy, no health config, and no
	// scheduler defaults to exec: the real service replaces ezx and becomes
	// PID 1 (the common single-process container case). Multi-root, non-leaf,
	// restart, health, or scheduled nodes stay supervised.
	execDefault := len(chain.Roots) == 1 &&
		len(chain.Roots[0].Children) == 0 &&
		chain.Roots[0].Restart == nil &&
		chain.Roots[0].Scheduler == nil
	for _, root := range chain.Roots {
		if err := s.runNode(ctx, root, execDefault); err != nil {
			return err
		}
	}
	return nil
}

// beginActive records that a supervised process is starting.
func (s *Service) beginActive() {
	s.mu.Lock()
	s.active++
	s.mu.Unlock()
}

// endActive records that a supervised process has finished.
func (s *Service) endActive() {
	s.mu.Lock()
	if s.active > 0 {
		s.active--
	}
	s.mu.Unlock()
}

// hasActiveSiblings reports whether any supervised process is still running.
func (s *Service) hasActiveSiblings() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active > 0
}

// runNode drives a single ProcessNode through its lifecycle. execDefault is
// true when this node is a lone leaf root that should exec by default.
func (s *Service) runNode(ctx context.Context, node domain.ProcessNode, execDefault bool) error {
	// Start children that do NOT require parent readiness first (parallel with
	// the parent), mirroring the original Execute ordering.
	if err := s.runChildren(ctx, node, false); err != nil {
		return err
	}

	// Provision files before starting the process.
	if err := repository.ProvisionFiles(node.Files); err != nil {
		s.log.Error("[%s] file provisioning failed: %v", node.Name, err)
		return err
	}

	// Build arguments from the process environment, then create the handle so
	// the enriched arguments reach the spawned process.
	env := os.Environ()
	args, err := repository.BuildArgs(node.Process, env)
	if err != nil {
		s.log.Error("[%s] argument build failed: %v", node.Name, err)
		return err
	}
	node.Process.Arguments = args

	// exec and Health are mutually exclusive: exec replaces PID 1 (ezx
	// vanishes), health requires ezx to stay alive. execDefault makes a lone
	// leaf root exec unless it opts into supervision via Health or Scheduler.
	execNode := node.Exec || (execDefault && node.Health == nil && node.Scheduler == nil)
	if node.Exec && node.Health != nil {
		return fmt.Errorf("node %q cannot have both Exec and Health set", node.Name)
	}

	// An Exec node replaces PID 1 with its process; ezx disappears, so there
	// must be no still-supervised siblings.
	if execNode {
		if s.hasActiveSiblings() {
			return fmt.Errorf("exec node %q cannot have active siblings being supervised", node.Name)
		}
		s.log.Info("[%s] exec'ing to become PID 1", node.Name)
		return repository.Exec(node.Process, env)
	}

	// A scheduled node runs its Process on a cron ticker (with manual triggers)
	// instead of once. It is supervised and drained like a regular process.
	if node.Scheduler != nil {
		if node.Exec || node.Health != nil {
			return fmt.Errorf("node %q cannot combine Scheduler with Exec or Health", node.Name)
		}
		return s.runScheduled(ctx, node)
	}

	proc := s.proc(node)
	s.log.Info("[%s] starting lifecycle", node.Name)
	s.beginActive()
	defer s.endActive()

	lc := domain.LogConfig{Stdout: domain.LogDestStdout, Stderr: domain.LogDestStderr}
	if node.Process.Log != nil {
		lc = *node.Process.Log
	}

	if err := proc.Start(ctx, env, lc); err != nil {
		s.log.Error("[%s] start failed: %v", node.Name, err)
		return err
	}
	s.log.Info("[%s] started (pid=%d)", node.Name, proc.PID())

	// Relay selected signals from ezx (PID 1) to the child process group.
	var fwd *repository.Forwarder
	if len(node.ForwardSignals) > 0 {
		sigs, ferr := repository.ResolveForwardSignals(node.ForwardSignals)
		if ferr != nil {
			s.log.Error("[%s] invalid forward signal: %v", node.Name, ferr)
			return ferr
		}
		fwd = repository.NewForwarder(proc.PID(), sigs)
		fwd.Start(ctx)
		s.log.Info("[%s] forwarding signals %v to pgid %d", node.Name, node.ForwardSignals, proc.PID())
		defer fwd.Stop()
	}

	// Drive readiness for the node's lifetime: reset it, then poll the node's
	// probe (Health.ReadyProbe, falling back to Readiness) and report the
	// result on the injected health service so /readyz reflects it.
	var pollCancel context.CancelFunc
	if node.Health != nil && s.health != nil {
		s.health.SetReady(false)
		probe := node.Health.ReadyProbe
		if probe == nil {
			probe = node.Readiness
		}
		var pollCtx context.Context
		pollCtx, pollCancel = context.WithCancel(ctx)
		go s.pollReadiness(pollCtx, probe)
		defer pollCancel()
	}

	// Wait for readiness before exposing to children that need it.
	if node.Readiness != nil {
		s.log.Info("[%s] waiting for readiness", node.Name)
		ready, err := repository.Check(ctx, *node.Readiness)
		if err != nil {
			s.log.Warn("[%s] readiness probe error: %v", node.Name, err)
		}
		if !ready {
			s.log.Error("[%s] never became ready", node.Name)
		}
		if node.Health != nil && s.health != nil {
			s.health.SetReady(ready)
		}
	}

	// Start children that require parent readiness now that the parent is up.
	if err := s.runChildren(ctx, node, true); err != nil {
		return err
	}

	return s.supervise(ctx, node, proc)
}

// runChildren starts either the need-parent-ready or the no-wait children.
func (s *Service) runChildren(ctx context.Context, node domain.ProcessNode, needReady bool) error {
	for _, child := range node.Children {
		if child.NeedParentReady == needReady {
			if err := s.runNode(ctx, child, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// pollReadiness polls the probe and reports readiness on the health service
// until ctx is cancelled or the probe is nil.
func (s *Service) pollReadiness(ctx context.Context, probe *domain.Probe) {
	if probe == nil {
		return
	}
	interval := probe.Interval
	if interval <= 0 {
		interval = time.Second
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		ready, err := repository.Check(ctx, *probe)
		if err != nil {
			s.log.Warn("health probe error: %v", err)
		}
		s.health.SetReady(ready)
	}
}

// supervise waits for the process, applying the restart policy and handling
// graceful drain on context cancellation.
func (s *Service) supervise(ctx context.Context, node domain.ProcessNode, proc process.ProcessRepository) error {
	shutdown := node.Shutdown
	if shutdown == nil {
		sig := os.Signal(syscall.SIGTERM)
		shutdown = &domain.ShutdownConfig{Signal: sig, Timeout: 30 * time.Second, ForceKill: true}
	}
	if shutdown.Signal == nil {
		shutdown.Signal = syscall.SIGTERM
	}
	if shutdown.Timeout <= 0 {
		shutdown.Timeout = 30 * time.Second
	}

	retries := 0
	for {
		select {
		case <-ctx.Done():
			s.log.Info("[%s] context cancelled, draining", node.Name)
			return s.drain(ctx, proc, *shutdown)
		case <-proc.Done():
			code, err := proc.Wait()
			s.log.Info("[%s] process exited (code=%d)", node.Name, code)

			shouldRestart := s.shouldRestart(node, code)
			if !shouldRestart {
				return err
			}
			if node.Restart != nil && node.Restart.MaxRetries > 0 && retries >= node.Restart.MaxRetries {
				s.log.Warn("[%s] restart retries exhausted", node.Name)
				return err
			}
			retries++
			backoff := s.backoff(node)
			s.log.Info("[%s] restarting in %v", node.Name, backoff)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			// Build a fresh handle: a ProcessRepository is single-use (its Start
			// is idempotent), so restarting requires a new one per attempt.
			proc = s.proc(node)
			if err := proc.Start(ctx, os.Environ(), s.logConfig(node)); err != nil {
				return err
			}
			s.log.Info("[%s] restarted (pid=%d)", node.Name, proc.PID())
		}
	}
}

func (s *Service) shouldRestart(node domain.ProcessNode, code int) bool {
	if node.Restart == nil {
		return false
	}
	switch node.Restart.Mode {
	case domain.RestartAlways:
		return true
	case domain.RestartOnFailure:
		return code != 0
	default: // RestartNever
		return false
	}
}

func (s *Service) backoff(node domain.ProcessNode) time.Duration {
	if node.Restart != nil && node.Restart.Backoff > 0 {
		return node.Restart.Backoff
	}
	return time.Second
}

func (s *Service) logConfig(node domain.ProcessNode) domain.LogConfig {
	if node.Process.Log != nil {
		return *node.Process.Log
	}
	return domain.LogConfig{Stdout: domain.LogDestStdout, Stderr: domain.LogDestStderr}
}

// registerTrigger records the trigger for a scheduled node so scripts can fire
// it by name (scheduler.trigger("backup-full")).
func (s *Service) registerTrigger(name string, t *domain.Trigger) {
	s.mu.Lock()
	s.triggers[name] = t
	s.mu.Unlock()
}

// unregisterTrigger removes the trigger for a scheduled node on drain.
func (s *Service) unregisterTrigger(name string) {
	s.mu.Lock()
	delete(s.triggers, name)
	s.mu.Unlock()
}

// Trigger fires the scheduled node's tick action immediately (fire-and-forget,
// skip if a tick is already running). Returns false if no such node exists or
// it is drained.
func (s *Service) Trigger(name string) bool {
	s.mu.Lock()
	t, ok := s.triggers[name]
	s.mu.Unlock()
	if !ok {
		return false
	}
	return t.Fire()
}

// InFlight reports whether the scheduled node currently has a tick running.
// Returns false if no such node exists.
func (s *Service) InFlight(name string) bool {
	s.mu.Lock()
	t, ok := s.triggers[name]
	s.mu.Unlock()
	if !ok {
		return false
	}
	return t.InFlight()
}

// Scheduled reports whether a scheduled node with the given name exists.
func (s *Service) Scheduled(name string) bool {
	s.mu.Lock()
	_, ok := s.triggers[name]
	s.mu.Unlock()
	return ok
}

// runScheduled drives a scheduled node: it provisions files, registers a
// trigger for the node, and runs a serial loop that fires the node's Process
// on each cron tick or manual trigger. It blocks until ctx is cancelled, then
// drains any in-flight tick. All tick processes go through the same reaper-
// registered ProcessRepository, so they are zombie-safe and drainable.
func (s *Service) runScheduled(ctx context.Context, node domain.ProcessNode) error {
	cfg := node.Scheduler
	loc := time.Local
	if cfg.Schedule.Timezone != "" {
		if l, err := time.LoadLocation(cfg.Schedule.Timezone); err == nil {
			loc = l
		} else {
			s.log.Warn("[%s] unknown timezone %q, using local", node.Name, cfg.Schedule.Timezone)
		}
	}
	cron, err := repository.ParseCron(cfg.Schedule.Expression)
	if err != nil {
		s.log.Error("[%s] invalid cron %q: %v", node.Name, cfg.Schedule.Expression, err)
		return err
	}
	s.mu.Lock()
	s.schedules[node.Name] = cron
	s.mu.Unlock()

	minInterval := cfg.MinInterval
	if minInterval <= 0 {
		minInterval = time.Minute
	}

	shutdown := s.resolveShutdown(node)
	env := os.Environ()
	lc := s.logConfig(node)

	done := make(chan struct{})
	trigger := domain.NewTrigger(done)
	s.registerTrigger(node.Name, trigger)
	defer s.unregisterTrigger(node.Name)
	defer close(done)

	s.log.Info("[%s] scheduler starting (cron=%q tz=%s minInterval=%v)",
		node.Name, cfg.Schedule.Expression, cfg.Schedule.Timezone, minInterval)

	// Initial delay before the first cron evaluation.
	select {
	case <-ctx.Done():
		return nil
	case <-time.After(cfg.InitialDelay):
	case <-trigger.C(): // a manual trigger during the initial delay fires early
		return s.runTick(ctx, node, shutdown, env, lc)
	}

	var lastRun time.Time
	for {
		now := time.Now().In(loc)
		next := cron.Next(now)
		var timer <-chan time.Time
		wait := next.Sub(now)
		if wait < 0 {
			wait = 0
		}
		timer = time.After(wait)

		select {
		case <-ctx.Done():
			s.log.Info("[%s] context cancelled, draining scheduler", node.Name)
			return nil
		case <-timer:
			if now.Before(next) {
				// timer fired slightly early (clock); recompute on next pass.
				continue
			}
			if time.Since(lastRun) < minInterval {
				continue
			}
			if err := s.runTick(ctx, node, shutdown, env, lc); err != nil {
				return err
			}
			lastRun = time.Now()
		case <-trigger.C():
			if time.Since(lastRun) < minInterval {
				continue
			}
			if err := s.runTick(ctx, node, shutdown, env, lc); err != nil {
				return err
			}
			lastRun = time.Now()
		}
	}
}

// resolveShutdown returns the node's ShutdownConfig with sane defaults
// (SIGTERM, 30s timeout, force-kill enabled), mirroring supervise.
func (s *Service) resolveShutdown(node domain.ProcessNode) domain.ShutdownConfig {
	shutdown := node.Shutdown
	if shutdown == nil {
		sig := os.Signal(syscall.SIGTERM)
		return domain.ShutdownConfig{Signal: sig, Timeout: 30 * time.Second, ForceKill: true}
	}
	if shutdown.Signal == nil {
		shutdown.Signal = syscall.SIGTERM
	}
	if shutdown.Timeout <= 0 {
		shutdown.Timeout = 30 * time.Second
	}
	return *shutdown
}

// runTick spawns the scheduled node's Process once, runs it to completion
// (with per-tick restart), and drains it on ctx cancellation. It is serialized
// via the trigger's in-flight slot so only one tick runs at a time.
func (s *Service) runTick(ctx context.Context, node domain.ProcessNode, shutdown domain.ShutdownConfig, env []string, lc domain.LogConfig) error {
	// Gate: skip the tick while the probe fails (e.g. not-primary during a
	// backup). The loop continues, so the next schedule/trigger re-checks.
	if node.Scheduler.Gate != nil {
		ok, err := repository.Check(ctx, *node.Scheduler.Gate)
		if err != nil {
			s.log.Warn("[%s] scheduler gate error: %v", node.Name, err)
		}
		if !ok {
			s.log.Debug("[%s] scheduler gate failed; skipping tick", node.Name)
			return nil
		}
	}

	// The trigger's in-flight slot guards concurrent ticks. We hold it for the
	// whole tick so Fire() drops requests while one is running.
	trigger := s.triggerFor(node.Name)
	if trigger == nil || !trigger.Acquire() {
		return nil
	}
	defer trigger.Release()

	s.beginActive()
	defer s.endActive()

	proc := s.proc(node)
	s.log.Info("[%s] tick starting (pid=%d)", node.Name, proc.PID())
	if err := proc.Start(ctx, env, lc); err != nil {
		s.log.Error("[%s] tick start failed: %v", node.Name, err)
		return err
	}

	// Run the tick to completion, applying per-tick restart.
	retries := 0
	for {
		select {
		case <-ctx.Done():
			s.log.Info("[%s] draining in-flight tick", node.Name)
			return s.drain(ctx, proc, shutdown)
		case <-proc.Done():
			code, werr := proc.Wait()
			s.log.Info("[%s] tick finished (code=%d)", node.Name, code)
			shouldRestart := s.shouldRestart(node, code)
			if !shouldRestart {
				return werr
			}
			if node.Restart != nil && node.Restart.MaxRetries > 0 && retries >= node.Restart.MaxRetries {
				s.log.Warn("[%s] tick restart retries exhausted", node.Name)
				return werr
			}
			retries++
			backoff := s.backoff(node)
			s.log.Info("[%s] restarting tick in %v", node.Name, backoff)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			proc = s.proc(node)
			if err := proc.Start(ctx, env, lc); err != nil {
				return err
			}
		}
	}
}

// triggerFor returns the live trigger for a scheduled node, or nil.
func (s *Service) triggerFor(name string) *domain.Trigger {
	s.mu.Lock()
	t := s.triggers[name]
	s.mu.Unlock()
	return t
}

// drain gracefully stops the process: sends the shutdown signal, waits for the
// timeout, then force-kills if enabled.
func (s *Service) drain(ctx context.Context, proc process.ProcessRepository, cfg domain.ShutdownConfig) error {
	if err := proc.Signal(cfg.Signal); err != nil {
		s.log.Warn("signal failed: %v", err)
	}
	timer := time.NewTimer(cfg.Timeout)
	defer timer.Stop()
	select {
	case <-proc.Done():
		return nil
	case <-timer.C:
		if cfg.ForceKill {
			s.log.Warn("graceful shutdown timed out, force-killing")
			return proc.Kill()
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
