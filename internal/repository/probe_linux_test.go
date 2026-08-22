//go:build linux

package repository

import (
	"context"
	"os"
	"testing"

	"github.com/supanadit/ezx/domain"
)

func TestProcessRunningAndZombies(t *testing.T) {
	// The test binary itself is a running process.
	if !ProcessRunning("probe_linux.test") {
		// comm may be truncated to 15 chars; accept a substring match on "probe".
		if !ProcessRunning("probe") {
			t.Fatalf("ProcessRunning should detect the running test process")
		}
	}
	// /proc must be mounted on Linux.
	if os.Getpid() <= 0 {
		t.Fatal("bad pid")
	}
	_ = ZombieCount() // must not panic
}

func TestDiskFree(t *testing.T) {
	dir := t.TempDir()
	pct, err := DiskFreePercent(dir)
	if err != nil {
		t.Fatalf("DiskFreePercent: %v", err)
	}
	if pct < 0 || pct > 100 {
		t.Fatalf("DiskFreePercent = %d, want 0-100", pct)
	}
	mb, err := DiskFreeMB(dir)
	if err != nil {
		t.Fatalf("DiskFreeMB: %v", err)
	}
	if mb < 0 {
		t.Fatalf("DiskFreeMB = %d, want >= 0", mb)
	}
}

func TestExecExpect(t *testing.T) {
	// Exit code is 0 regardless of output; ExecExpect gates on stdout.
	ready, err := Check(context.Background(), domain.Probe{
		Type:       domain.ProbeTypeExec,
		Exec:       []string{"/bin/sh", "-c", `echo "f"`},
		ExecExpect: "f",
	})
	if err != nil || !ready {
		t.Fatalf("exec probe with ExecExpect 'f' ready=%v err=%v, want ready", ready, err)
	}

	notReady, err := Check(context.Background(), domain.Probe{
		Type:       domain.ProbeTypeExec,
		Exec:       []string{"/bin/sh", "-c", `echo "t"`},
		ExecExpect: "f",
	})
	if err != nil || notReady {
		t.Fatalf("exec probe with mismatched ExecExpect ready=%v err=%v, want not ready", notReady, err)
	}

	// Exact mode: trimmed stdout must equal the expectation.
	exact, err := Check(context.Background(), domain.Probe{
		Type:           domain.ProbeTypeExec,
		Exec:           []string{"/bin/sh", "-c", `printf " f "`},
		ExecExpect:     "f",
		ExecExpectExact: true,
	})
	if err != nil || !exact {
		t.Fatalf("exec probe exact match ready=%v err=%v, want ready", exact, err)
	}

	// Without ExecExpect, a zero-exit command is ready.
	plain, err := Check(context.Background(), domain.Probe{
		Type: domain.ProbeTypeExec,
		Exec: []string{"/bin/true"},
	})
	if err != nil || !plain {
		t.Fatalf("plain exec probe ready=%v err=%v, want ready", plain, err)
	}
}
