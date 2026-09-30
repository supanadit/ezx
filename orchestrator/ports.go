package orchestrator

import (
	"context"
	"os"
	"time"

	"github.com/supanadit/ezx/domain"
)

// The ports below are the driven-side capabilities the supervisor consumes,
// declared here (the consuming side, R2) so the module depends on interfaces,
// never on the adapter package. The composition root satisfies them with the OS
// adapter. Their signatures use only stdlib and domain types.

// FileProvisioner applies a node's declarative file-provision rules.
type FileProvisioner interface {
	Provision(files []domain.FileProvision) error
}

// ArgBuilder assembles a process's CLI arguments from its environment.
type ArgBuilder interface {
	Build(p domain.Process, environ []string) ([]string, error)
}

// ProcessExecer replaces the current process image with a node's process.
type ProcessExecer interface {
	Exec(p domain.Process, env []string) error
}

// ProbeChecker runs a readiness/health probe.
type ProbeChecker interface {
	Check(ctx context.Context, p domain.Probe) (bool, error)
}

// SignalForwarder resolves signal names and relays signals to a process group.
type SignalForwarder interface {
	Resolve(names []string) ([]os.Signal, error)
	StartForwarder(ctx context.Context, pgid int, sigs []os.Signal) func()
}

// CronParser compiles a cron expression into a next-time function.
type CronParser interface {
	Parse(expr string) (next func(after time.Time) time.Time, err error)
}

// Deps bundles the driven-side ports the supervisor needs.
type Deps struct {
	Files   FileProvisioner
	Args    ArgBuilder
	Exec    ProcessExecer
	Probes  ProbeChecker
	Signals SignalForwarder
	Cron    CronParser
}
