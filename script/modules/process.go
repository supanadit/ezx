package scriptmodules

import (
	"context"
	"os"
	"syscall"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/process"
)

// ProcessFactory constructs a ProcessRepository handle for a ProcessNode. It
// mirrors orchestrator.ProcessFactory so scripts can spawn processes.
type ProcessFactory func(node domain.ProcessNode) process.ProcessRepository

// ProcessModule exposes ezx.process: spawn(opts) starts a process from a JS
// object and returns a handle with wait/signal/kill/pid/done.
type ProcessModule struct {
	factory ProcessFactory
}

// NewProcessModule returns a ProcessModule backed by the given factory.
func NewProcessModule(factory ProcessFactory) *ProcessModule {
	return &ProcessModule{factory: factory}
}

// Spawn launches a process from a JS options object (binary, args, env,
// workingDir) and returns a script-visible process handle.
func (m *ProcessModule) Spawn(node domain.ProcessNode) *ProcessHandle {
	repo := m.factory(node)
	return &ProcessHandle{repo: repo}
}

// ProcessHandle wraps a process.ProcessRepository as the script-visible handle.
type ProcessHandle struct {
	repo process.ProcessRepository
}

// Start launches the process (idempotent).
func (h *ProcessHandle) Start(env []string) error {
	return h.repo.Start(context.Background(), env, domain.LogConfig{
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
