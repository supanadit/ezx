//go:build linux

package repository

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestResolveForwardSignals(t *testing.T) {
	sigs, err := ResolveForwardSignals([]string{"SIGUSR1", "SIGTERM", "USR1"})
	if err != nil {
		t.Fatalf("ResolveForwardSignals: %v", err)
	}
	if len(sigs) != 2 {
		t.Fatalf("len(sigs) = %d, want 2 (deduplicated)", len(sigs))
	}
	if _, ok := SignalName("BOGUS"); ok {
		t.Fatal("SignalName(BOGUS) should be false")
	}
	if _, err := ResolveForwardSignals([]string{"BOGUS"}); err == nil {
		t.Fatal("ResolveForwardSignals should error on unknown signal")
	}
}

func TestForwarderSendsToGroup(t *testing.T) {
	// Spawn a child that traps SIGUSR1 and reports it via an exit code.
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	cmd := exec.Command(sh, "-c", "trap 'exit 7' USR1; while :; do sleep 0.1; done")
	SetProcessGroupLeader(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	// Forward SIGUSR1 to the child's process group.
	f := NewForwarder(cmd.Process.Pid, []os.Signal{syscall.SIGUSR1})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.Start(ctx)

	// Give the forwarder a moment to install handlers.
	time.Sleep(50 * time.Millisecond)

	// Send SIGUSR1; the forwarder relays it to the child's group, and the
	// child's trap exits with code 7.
	_ = signalToGroup(cmd.Process.Pid, syscall.SIGUSR1)

	done := make(chan error, 1)
	go func() { _, err := cmd.Process.Wait(); done <- err }()
	select {
	case <-done:
		if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() != 7 {
			t.Fatalf("child exit = %d, want 7 (trapped signal forwarded)", cmd.ProcessState.ExitCode())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("child did not exit after forwarded signal")
	}
	f.Stop()
}
