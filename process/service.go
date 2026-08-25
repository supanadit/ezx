// Package process defines the Port for managing a single OS process. A
// ProcessRepository is both a factory for a specific ProcessNode and the handle
// to that process once started — mirroring os/exec.Cmd (construct, Start, then
// Wait/Signal/Kill). The tree-walk and lifecycle orchestration that composes
// multiple processes lives in the orchestrator package, not here.
package process

import (
	"context"
	"os"

	"github.com/supanadit/ezx/domain"
)

// ProcessRepository is the contract a process adapter implements. It is
// constructed per ProcessNode and is the handle to that node's process.
type ProcessRepository interface {
	// Start launches the configured process with the given environment and log
	// routing. Must be called once before Wait/Signal/Kill.
	Start(ctx context.Context, env []string, lc domain.LogConfig) error
	// Wait blocks until the process exits and returns its exit code. Safe to
	// call from a single goroutine.
	Wait() (int, error)
	// Signal sends a signal to the process (e.g. for graceful shutdown).
	Signal(sig os.Signal) error
	// Kill force-terminates the process.
	Kill() error
	// PID returns the running process's PID, or 0 if it has not been started.
	PID() int
	// Done closes when the process exits. It is safe to select on.
	Done() <-chan struct{}
	// Output returns the captured stdout and stderr for a process started with
	// LogDestCapture. Empty strings when capture was not requested.
	Output() (stdout, stderr string)
}
