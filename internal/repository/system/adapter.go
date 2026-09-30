package system

import (
	"context"
	"os"
	"time"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/internal/repository"
)

// Adapter bundles the driven-side OS operations the supervisor and the
// scripting delivery layer consume: file provisioning, argument building,
// process exec, probe checks, signal forwarding, cron parsing, and the file
// editor. It is one technology adapter (the local OS); the composition root
// constructs it and satisfies each consumer's locally declared port with it.
//
// The method signatures deliberately use only stdlib and domain types so this
// adapter never has to import a use-case module to name a port.
type Adapter struct{}

// NewAdapter returns an OS adapter.
func NewAdapter() *Adapter { return &Adapter{} }

// Provision applies the node's declarative file-provision rules.
func (a *Adapter) Provision(files []domain.FileProvision) error { return ProvisionFiles(files) }

// Build assembles CLI arguments from a process config and environment.
func (a *Adapter) Build(p domain.Process, environ []string) ([]string, error) {
	return BuildArgs(p, environ)
}

// Exec replaces the current process image with the configured program.
func (a *Adapter) Exec(p domain.Process, env []string) error { return Exec(p, env) }

// Check runs a readiness/health probe.
func (a *Adapter) Check(ctx context.Context, p domain.Probe) (bool, error) {
	return Check(ctx, p)
}

// Resolve converts signal names to a deduplicated signal set.
func (a *Adapter) Resolve(names []string) ([]os.Signal, error) {
	return ResolveForwardSignals(names)
}

// StartForwarder relays sigs received by ezx to the process group pgid and
// returns a stop function.
func (a *Adapter) StartForwarder(ctx context.Context, pgid int, sigs []os.Signal) func() {
	f := NewForwarder(pgid, sigs)
	f.Start(ctx)
	return f.Stop
}

// Parse compiles a cron expression into a next-time function.
func (a *Adapter) Parse(expr string) (func(after time.Time) time.Time, error) {
	c, err := repository.ParseCron(expr)
	if err != nil {
		return nil, err
	}
	return c.Next, nil
}

// OpenFileEditor opens a file-editor handle for the script delivery layer.
func (a *Adapter) OpenFileEditor(path string) domain.FileEditor {
	return OpenFileEditor(path)
}
