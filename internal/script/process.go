package script

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/internal/repository"
	"github.com/supanadit/ezx/process"
	"github.com/supanadit/ezx/runtime"
)

// ProcessFactory constructs a ProcessRepository handle for a ProcessNode. It
// mirrors orchestrator.ProcessFactory so scripts can spawn processes.
type ProcessFactory func(node domain.ProcessNode) process.ProcessRepository

// ProcessModule exposes ezx.process: spawn(opts) starts a process from a JS
// object and returns a handle with wait/signal/kill/pid/done. It carries the
// script's cancellation context so spawned processes are interrupted when the
// app shuts down (e.g. SIGTERM). It also exposes run/capture/shell one-shot
// helpers and, when an invoker is provided, optional per-line streaming
// callbacks.
type ProcessModule struct {
	ctx     context.Context
	factory ProcessFactory
	inv     runtime.Invoker
	gate    runtime.Gate
}

// NewProcessModule returns a ProcessModule backed by the given factory,
// interrupting spawned processes when ctx is cancelled. inv is used to deliver
// streaming callbacks; it may be nil when the engine cannot call back. gate is
// the scripting engine's entry gate: run/capture/shell park it while blocked
// on the child process so its onStdout/onStderr callbacks are admitted safely.
func NewProcessModule(ctx context.Context, factory ProcessFactory, inv runtime.Invoker, gate runtime.Gate) *ProcessModule {
	return &ProcessModule{ctx: ctx, factory: factory, inv: inv, gate: gate}
}

// Spawn launches a process from a JS options object (binary, args, env,
// workingDir) and returns a script-visible process handle.
func (m *ProcessModule) Spawn(node domain.ProcessNode) *ProcessHandle {
	repo := m.factory(node)
	return &ProcessHandle{ctx: m.ctx, repo: repo}
}

// ProcessHandle wraps a process.ProcessRepository as the script-visible handle.
type ProcessHandle struct {
	ctx  context.Context
	repo process.ProcessRepository

	cancelOnce sync.Once
	cancelDone chan struct{}
}

// Exec replaces the current process image (PID 1) with the given process via
// syscall.Exec. This is the final, long-running entrypoint process (e.g. the
// postgres server). It never returns on success.
func (m *ProcessModule) Exec(node domain.ProcessNode) error {
	return repository.Exec(node.Process, os.Environ())
}

// Start launches the process (idempotent) and begins watching the module's
// cancellation context, so a spawned process is interrupted when the app shuts
// down. The watcher exits as soon as the process does (it selects on the
// repo's Done), so unlike os/exec's CommandContext watcher it cannot leak a
// goroutine per spawn; and it signals rather than kills, so the caller's own
// cooperative shutdown still wins.
func (h *ProcessHandle) Start(env []string) error {
	if err := h.repo.Start(h.ctx, env, domain.LogConfig{
		Stdout: domain.LogDestStdout,
		Stderr: domain.LogDestStderr,
	}); err != nil {
		return err
	}
	h.cancelOnce.Do(func() {
		h.cancelDone = make(chan struct{})
		go h.watchCancel()
	})
	return nil
}

// watchCancel interrupts the process on context cancellation. A process that
// ignores the signal is left to the orchestrator's drain timeout or to exit on
// its own; this path never escalates to SIGKILL.
func (h *ProcessHandle) watchCancel() {
	defer close(h.cancelDone)
	select {
	case <-h.ctx.Done():
		_ = h.repo.Signal(syscall.SIGTERM)
	case <-h.repo.Done():
	}
}

// Wait blocks until the process exits and returns its exit code.
func (h *ProcessHandle) Wait() (int, error) {
	code, err := h.repo.Wait()
	if h.cancelDone != nil {
		<-h.cancelDone
	}
	return code, err
}

// Signal sends a signal to the process by name (SIGTERM, SIGINT, SIGKILL...).
func (h *ProcessHandle) Signal(name string) error {
	sig, ok := parseSignal(name)
	if !ok {
		return nil
	}
	return h.repo.Signal(sig)
}

// Kill force-terminates the process.
func (h *ProcessHandle) Kill() error {
	return h.repo.Kill()
}

// PID returns the running process's PID, or 0 if not started.
func (h *ProcessHandle) PID() int {
	return h.repo.PID()
}

func parseSignal(name string) (os.Signal, bool) {
	switch name {
	case "SIGTERM", "TERM", "15":
		return syscall.SIGTERM, true
	case "SIGINT", "INT", "2":
		return syscall.SIGINT, true
	case "SIGKILL", "KILL", "9":
		return syscall.SIGKILL, true
	case "SIGQUIT", "QUIT":
		return syscall.SIGQUIT, true
	case "SIGHUP", "HUP":
		return syscall.SIGHUP, true
	default:
		return nil, false
	}
}

// runOpts mirrors the ProcessNode-shaped options accepted by process.run and
// process.capture. OnStdout/OnStderr are optional per-line streaming callbacks
// (delivered after the process completes); Check throws on a non-zero exit.
type runOpts struct {
	// Name is an optional label for the spawned process.
	Name string
	// Process holds the executable configuration.
	Process domain.Process
	// OnStdout is an optional JS callable invoked per captured stdout line.
	OnStdout any
	// OnStderr is an optional JS callable invoked per captured stderr line.
	OnStderr any
	// Timeout bounds the process lifetime in nanoseconds (0 = no bound). On
	// expiry the process is terminated with SIGKILL via context cancellation —
	// the native replacement for timeout(1).
	Timeout int64
	// Check throws on a non-zero exit.
	Check bool
}

// Run executes a one-shot process and returns its exit code. It spawns with
// stdout/stderr inherited (LogDestStdout/LogDestStderr), waits for completion,
// and returns the exit code as an int. When Check is true, a non-zero exit
// throws an Error including the code and binary path. Same options shape as
// process.spawn (name + process{...}).
func (m *ProcessModule) Run(opts runOpts) (int, error) {
	lc := domain.LogConfig{Stdout: domain.LogDestStdout, Stderr: domain.LogDestStderr}
	code, _, _, err := m.oneshot(opts, lc)
	if err != nil {
		return code, err
	}
	if opts.Check && code != 0 {
		return code, fmt.Errorf("process %q exited with code %d", opts.Process.BinaryPath, code)
	}
	return code, nil
}

// Capture executes a one-shot process, buffering its stdout/stderr, and returns
// { code, stdout, stderr }. Same options shape as process.run. When Check is
// true, a non-zero exit throws an Error including the code and stderr.
func (m *ProcessModule) Capture(opts runOpts) (map[string]any, error) {
	lc := domain.LogConfig{Stdout: domain.LogDestCapture, Stderr: domain.LogDestCapture}
	code, stdout, stderr, err := m.oneshot(opts, lc)
	if err != nil {
		return nil, err
	}
	if opts.Check && code != 0 {
		return nil, fmt.Errorf("process %q exited with code %d: %s", opts.Process.BinaryPath, code, stderr)
	}
	return map[string]any{"code": code, "stdout": stdout, "stderr": stderr}, nil
}

// Shell runs an explicit shell command via /bin/sh -c. The command string is
// the single-argument form; the options carry user/group/env filtering and an
// optional Check. Returns the exit code (Check throws on non-zero). This is the
// explicit escape hatch for genuine pipelines — prefer run/capture with an
// arguments array.
func (m *ProcessModule) Shell(cmd string, opts shellOpts) (int, error) {
	p := domain.Process{
		BinaryPath:       "/bin/sh",
		Arguments:        []string{"-c", cmd},
		User:             opts.User,
		Group:            opts.Group,
		WorkingDir:       opts.WorkingDir,
		Environment:      opts.Env,
		FilterEnv:        opts.FilterEnv,
		FilterEnvPattern: opts.FilterEnvPattern,
	}
	lc := domain.LogConfig{Stdout: domain.LogDestStdout, Stderr: domain.LogDestStderr}
	code, _, _, err := m.oneshot(runOpts{Name: opts.Name, Process: p}, lc)
	if err != nil {
		return code, err
	}
	if opts.Check && code != 0 {
		return code, fmt.Errorf("shell command exited with code %d", code)
	}
	return code, nil
}

// shellOpts holds the options for process.shell.
type shellOpts struct {
	Name             string
	User             string
	Group            string
	WorkingDir       string
	Env              []string
	FilterEnv        []string
	FilterEnvPattern []string
	Check            bool
}

// oneshot spawns the process, waits, returns the exit code and any captured
// stdout/stderr, and delivers streaming callbacks (if any) post-hoc. A positive
// opts.Timeout bounds the wait (the timeout(1) equivalent): the timer kills the
// process and the call fails with a "timed out" error.
//
// The timeout is enforced here, not by the process adapter, because the adapter
// deliberately spawns without a cancellation watcher (a ctx-triggered SIGKILL
// would pre-empt drain's cooperative shutdown signal -> timeout -> force-kill,
// and leak a goroutine per spawn while the reaper owns the wait). Kill is the
// only escalation a bounded one-shot needs.
//
// While blocked on the child it parks the scripting engine: the onStdout/
// onStderr callbacks run inside the VM from this goroutine, so the script frame
// must be suspended for them (see runtime.Gate).
func (m *ProcessModule) oneshot(opts runOpts, lc domain.LogConfig) (code int, stdout, stderr string, err error) {
	node := domain.ProcessNode{Name: opts.Name, Process: opts.Process}
	proc := m.factory(node)

	if err := proc.Start(m.ctx, os.Environ(), lc); err != nil {
		return -1, "", "", err
	}

	// Arm the timeout watchdog before parking. A goroutine is used rather than
	// time.AfterFunc because this goroutine is about to park inside the scripting
	// engine and block on proc.Wait() — it cannot wait for a timer callback. The
	// watchdog exits on proc.Done() (or after killing), so it is bounded by the
	// process lifetime or the timeout, never leaked. timedOut is read only after
	// the wait completes; the race detector confirms the handoff.
	timedOut := false
	var timedOutMu sync.Mutex
	if opts.Timeout > 0 {
		go func() {
			timer := time.NewTimer(time.Duration(opts.Timeout))
			defer timer.Stop()
			select {
			case <-timer.C:
				timedOutMu.Lock()
				timedOut = true
				timedOutMu.Unlock()
				_ = proc.Kill()
			case <-proc.Done():
			}
		}()
	}

	if m.gate != nil {
		m.gate.Park()
	}
	code, err = proc.Wait()
	if m.gate != nil {
		m.gate.Unpark()
	}
	timedOutMu.Lock()
	overtime := timedOut
	timedOutMu.Unlock()
	if overtime {
		return code, "", "", fmt.Errorf("process %q timed out", opts.Process.BinaryPath)
	}
	if err != nil {
		return code, "", "", err
	}
	if lc.Stdout == domain.LogDestCapture {
		stdout, stderr = proc.Output()
		deliverLines(m.inv, opts.OnStdout, stdout)
		deliverLines(m.inv, opts.OnStderr, stderr)
	}
	return code, stdout, stderr, nil
}

// Sleep blocks the script for the given duration in nanoseconds — the native
// replacement for sleep(1) (e.g. process.sleep(2e9) = 2 seconds). Sleeping
// does not block the Go runtime; the interrupted-context is not consumed.
// The wait parks the scripting engine, so scheduler ticks and lifecycle
// callbacks may run while the script sleeps.
func (m *ProcessModule) Sleep(ns int64) error {
	if ns < 0 {
		return fmt.Errorf("process.sleep: negative duration")
	}
	if m.gate != nil {
		m.gate.Park()
		defer m.gate.Unpark()
	}
	timer := time.NewTimer(time.Duration(ns))
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-m.ctx.Done():
		return m.ctx.Err()
	}
}

// deliverLines invokes fn once per non-empty line of out (post-hoc line
// splitting, the simple version). A nil invoker or nil fn is a no-op.
//
// Delivery is best-effort: it uses TryInvoker when the engine provides it, so
// a script that is not parked (or an engine that is already busy) skips the
// remaining lines instead of blocking this goroutine or re-entering a
// single-threaded VM that is already inside a callback.
func deliverLines(inv runtime.Invoker, fn any, out string) {
	if inv == nil || fn == nil || out == "" {
		return
	}
	try, ok := inv.(runtime.TryInvoker)
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if ok {
			if _, err := try.TryCall(fn, line); err != nil {
				return
			}
			continue
		}
		_, _ = inv.Call(fn, line)
	}
}
