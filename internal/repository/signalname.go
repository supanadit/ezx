package repository

import (
	"os"
	"syscall"
)

// This file holds the technology-agnostic signal-name catalog: mapping the
// string names scripts and configs use ("SIGUSR1", "USR1", "10") to os.Signal.
// It is a shared helper (stdlib only) so driven adapters — the stream/process
// logging paths and the app-owned file rotator — can resolve a reopen signal
// without depending on a domain-aware adapter.

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
