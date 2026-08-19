//go:build !linux

package system

import "context"

// Reaper is a no-op on non-Linux platforms; subreaper reaping is a container
// (Linux) concern. The ProcessRepository handle falls back to local cmd.Wait().
type Reaper struct{}

// NewReaper returns a no-op Reaper.
func NewReaper(logf func(string, ...any)) *Reaper {
	return &Reaper{}
}

// Start is a no-op.
func (r *Reaper) Start(ctx context.Context) error { return nil }

// Register returns a channel that never fires; the handle uses local waiting.
func (r *Reaper) Register(pid int) chan procExit { return nil }

// Stop is a no-op.
func (r *Reaper) Stop() {}
