package system

import (
	"context"
	"os"
	"os/signal"
	"sync"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/internal/repository"
)

// ResolveForwardSignals converts node signal names into a deduplicated set.
// An empty input returns the default set.
func ResolveForwardSignals(names []string) ([]os.Signal, error) {
	if len(names) == 0 {
		return repository.ForwardSignalSet, nil
	}
	seen := map[os.Signal]bool{}
	var out []os.Signal
	for _, n := range names {
		sig, ok := repository.SignalName(n)
		if !ok {
			return nil, domain.SignalForwardError{Name: n}
		}
		if !seen[sig] {
			seen[sig] = true
			out = append(out, sig)
		}
	}
	return out, nil
}

// Forwarder relays signals received by ezx (as PID 1) to a child process
// group, preserving dumb-init/tini signal-forwarding semantics.
type Forwarder struct {
	pgid int
	sigs []os.Signal
	ch   chan os.Signal
	done chan struct{}
	once sync.Once
}

// NewForwarder returns a Forwarder for the given process group and signals.
func NewForwarder(pgid int, sigs []os.Signal) *Forwarder {
	return &Forwarder{pgid: pgid, sigs: sigs, ch: make(chan os.Signal, 16), done: make(chan struct{})}
}

// Start begins forwarding signals to the process group.
func (f *Forwarder) Start(ctx context.Context) {
	signal.Notify(f.ch, f.sigs...)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-f.done:
				return
			case sig := <-f.ch:
				_ = signalToGroup(f.pgid, sig)
			}
		}
	}()
}

// Stop stops forwarding.
func (f *Forwarder) Stop() {
	f.once.Do(func() {
		signal.Stop(f.ch)
		close(f.done)
	})
}
