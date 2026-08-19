//go:build linux

package system

import (
	"context"
	"os"
	"os/signal"
	"sync"

	"golang.org/x/sys/unix"
)

// Reaper is the single process-wide reaper (PID 1 init role). It installs
// PR_SET_CHILD_SUBREAPER so orphaned grandchildren reparent to ezx, traps
// SIGCHLD, and calls wait4(-1, WNOHANG) to reap every exited child — both
// direct children and adopted orphans — preventing zombies. It is the only
// waiter on child PIDs, so the ProcessRepository handle does not race with it.
type Reaper struct {
	mu      sync.Mutex
	waits   map[int]chan procExit
	pending map[int]procExit // recently reaped, awaiting a late subscriber
	reaped  chan procExit
	logf    func(format string, args ...any)
	sigCh   chan os.Signal
	stop    chan struct{}
	stopped sync.Once
}

// NewReaper returns a Reaper. logf is optional (defaults to discard).
func NewReaper(logf func(string, ...any)) *Reaper {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Reaper{
		waits:   make(map[int]chan procExit),
		pending: make(map[int]procExit),
		reaped:  make(chan procExit, 64),
		logf:    logf,
	}
}

// Start installs the subreaper and begins reaping. It returns once the
// SIGCHLD handler and wait loop are active.
func (r *Reaper) Start(ctx context.Context) error {
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return err
	}
	r.sigCh = make(chan os.Signal, 1)
	r.stop = make(chan struct{})
	signal.Notify(r.sigCh, unix.SIGCHLD)

	go r.waitLoop(ctx)
	go r.dispatchLoop(ctx)
	return nil
}

// Register subscribes for the exit of the given PID. The returned channel
// receives a single procExit when the PID is reaped (or immediately if it was
// already reaped before registration), then is closed.
func (r *Reaper) Register(pid int) chan procExit {
	ch := make(chan procExit, 1)
	r.mu.Lock()
	if exit, ok := r.pending[pid]; ok {
		delete(r.pending, pid)
		r.mu.Unlock()
		ch <- exit
		close(ch)
		return ch
	}
	r.waits[pid] = ch
	r.mu.Unlock()
	return ch
}

// Stop uninstalls the subreaper and stops reaping.
func (r *Reaper) Stop() {
	r.stopped.Do(func() {
		if r.sigCh != nil {
			signal.Stop(r.sigCh)
		}
		if r.stop != nil {
			close(r.stop)
		}
	})
}

// waitLoop waits for children and forwards reaped outcomes to the dispatcher.
func (r *Reaper) waitLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		case <-r.sigCh:
			r.reapAvailable(ctx)
		}
	}
}

// reapAvailable reaps all currently-exited children (non-blocking).
func (r *Reaper) reapAvailable(ctx context.Context) {
	for {
		var status unix.WaitStatus
		pid, err := unix.Wait4(-1, &status, unix.WNOHANG, nil)
		if pid <= 0 {
			return
		}
		exit := procExit{code: exitCode(status)}
		if err != nil {
			exit.err = err
		}
		select {
		case r.reaped <- exit:
		case <-ctx.Done():
			return
		default:
		}
		r.mu.Lock()
		ch, ok := r.waits[pid]
		if ok {
			delete(r.waits, pid)
		} else {
			// No subscriber yet (Start→Register race). Cache it so a late
			// Register delivers the outcome; otherwise the handle would hang.
			r.pending[pid] = exit
		}
		r.mu.Unlock()
		if ch != nil {
			ch <- exit
			close(ch)
		} else if ok == false {
			r.logf("reaped pid=%d code=%d (cached for late subscriber)", pid, exit.code)
		}
	}
}

// dispatchLoop drains the reaped channel until Stop/ctx cancel.
func (r *Reaper) dispatchLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		case <-r.reaped:
		}
	}
}

// exitCode extracts the process exit code from a WaitStatus.
func exitCode(status unix.WaitStatus) int {
	if status.Exited() {
		return status.ExitStatus()
	}
	if status.Signaled() {
		// Mirror shell conventions: 128 + signal number.
		return 128 + int(status.Signal())
	}
	return -1
}
