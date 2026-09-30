// Package rotator defines the Port for rotating log files that the supervised
// process owns and writes itself. Unlike the ezx-owned stdout/stderr streams
// (which ezx opens and can rotate unconditionally), these files are held open by
// the app, so a rotation is a rename followed by a reopen signal to the app's
// process — a concern that is coupled to supervision.
//
// The concrete rotator (watcher + archive engine) lives in an adapter
// (internal/repository/logrotate); the orchestrator depends only on this Port
// and starts one session per node for the node's supervised lifetime.
package rotator

import (
	"context"
	"os"

	"github.com/supanadit/ezx/domain"
)

// Target is the supervised process a rotation pass sends its reopen signal to.
// *system.ProcessRepository satisfies it structurally; the orchestrator wraps
// the live handle so a restart is reflected without restarting the watcher.
type Target interface {
	// PID returns the process's PID, or 0 when it is not running.
	PID() int
	// Signal sends a signal (e.g. USR1) to the process.
	Signal(sig os.Signal) error
}

// Rotator supervises the app-owned log files of one node: it watches the
// configured directories and rotates files as writes push them past their
// thresholds until ctx is cancelled.
type Rotator interface {
	// Run watches and rotates until ctx is done. It returns when ctx is
	// cancelled. Configuration and I/O errors are logged and skipped — a
	// rotation failure never aborts the supervised process.
	Run(ctx context.Context, cfg domain.LogRotateConfig, target Target) error
}

// Factory constructs a Rotator. It is injected by the composition root so the
// orchestrator never imports a concrete watcher implementation.
type Factory func() Rotator
