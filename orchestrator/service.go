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

	mu     sync.Mutex
	active int
}

// NewService builds a supervisor from the injected Ports. health is optional
// (nil means health/readiness is not surfaced to the delivery layer).
func NewService(
	proc ProcessFactory,
	log logger.Logger,
	health domain.HealthService,
) *Service {
	return &Service{
		proc:   proc,
		log:    log,
		health: health,
	}
}

// Run executes every root in the chain and supervises the full dependency tree.
func (s *Service) Run(ctx context.Context, chain domain.ProcessChain) error {
	// A lone leaf root with no restart policy and no health config defaults to
	// exec: the real service replaces ezx and becomes PID 1 (the common
	// single-process container case). Multi-root, non-leaf, restart, or health
	// nodes stay supervised.
	execDefault := len(chain.Roots) == 1 &&
		len(chain.Roots[0].Children) == 0 &&
		chain.Roots[0].Restart == nil
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
	// leaf root exec unless it opts into supervision via Health.
	execNode := node.Exec || (execDefault && node.Health == nil)
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
