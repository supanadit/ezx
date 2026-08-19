package repository

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/supanadit/ezx/domain"
)

// ForwardSignalSet is the default set of signals relayed to a child process
// group when a node opts into full forwarding. It mirrors what dumb-init/tini
// forward so that PID 1 semantics reach the supervised process.
var ForwardSignalSet = []os.Signal{
	syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP,
	syscall.SIGQUIT, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGWINCH,
}

// SignalName parses a signal name like "SIGUSR1", "USR1", or "10" to a signal.
func SignalName(name string) (os.Signal, bool) {
	switch name {
	case "SIGTERM", "TERM", "15":
		return syscall.SIGTERM, true
	case "SIGINT", "INT", "2":
		return syscall.SIGINT, true
	case "SIGHUP", "HUP", "1":
		return syscall.SIGHUP, true
	case "SIGQUIT", "QUIT", "3":
		return syscall.SIGQUIT, true
	case "SIGUSR1", "USR1", "10":
		return syscall.SIGUSR1, true
	case "SIGUSR2", "USR2", "12":
		return syscall.SIGUSR2, true
	case "SIGWINCH", "WINCH", "28":
		return syscall.SIGWINCH, true
	default:
		return nil, false
	}
}

// ResolveForwardSignals converts node signal names into a deduplicated set.
// An empty input returns the default set.
func ResolveForwardSignals(names []string) ([]os.Signal, error) {
	if len(names) == 0 {
		return ForwardSignalSet, nil
	}
	seen := map[os.Signal]bool{}
	var out []os.Signal
	for _, n := range names {
		sig, ok := SignalName(n)
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
