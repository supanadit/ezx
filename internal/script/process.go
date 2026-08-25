package script

import (
	"context"
	"fmt"
	"os"
	"strings"
	"syscall"

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
}

// NewProcessModule returns a ProcessModule backed by the given factory,
// interrupting spawned processes when ctx is cancelled. inv is used to deliver
// streaming callbacks; it may be nil when the engine cannot call back.
func NewProcessModule(ctx context.Context, factory ProcessFactory, inv runtime.Invoker) *ProcessModule {
	return &ProcessModule{ctx: ctx, factory: factory, inv: inv}
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
}

// Exec replaces the current process image (PID 1) with the given process via
// syscall.Exec. This is the final, long-running entrypoint process (e.g. the
// postgres server). It never returns on success.
func (m *ProcessModule) Exec(node domain.ProcessNode) error {
	return repository.Exec(node.Process, os.Environ())
}

// Start launches the process (idempotent).
func (h *ProcessHandle) Start(env []string) error {
	return h.repo.Start(h.ctx, env, domain.LogConfig{
		Stdout: domain.LogDestStdout,
		Stderr: domain.LogDestStderr,
	})
}

// Wait blocks until the process exits and returns its exit code.
func (h *ProcessHandle) Wait() (int, error) {
	return h.repo.Wait()
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
// stdout/stderr, and delivers streaming callbacks (if any) post-hoc.
func (m *ProcessModule) oneshot(opts runOpts, lc domain.LogConfig) (code int, stdout, stderr string, err error) {
	node := domain.ProcessNode{Name: opts.Name, Process: opts.Process}
	proc := m.factory(node)
	if err := proc.Start(m.ctx, os.Environ(), lc); err != nil {
		return -1, "", "", err
	}
	code, err = proc.Wait()
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

// deliverLines invokes fn once per non-empty line of out (post-hoc line
// splitting, the simple version). A nil invoker or nil fn is a no-op.
func deliverLines(inv runtime.Invoker, fn any, out string) {
	if inv == nil || fn == nil || out == "" {
		return
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		_, _ = inv.Call(fn, line)
	}
}
