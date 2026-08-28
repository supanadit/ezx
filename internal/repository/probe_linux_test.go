//go:build linux

package repository

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/supanadit/ezx/domain"
)

func TestProcessRunningAndZombies(t *testing.T) {
	// Spawn a known long-running process and assert ProcessRunning detects it
	// by its comm. Deterministic — does not depend on the test binary's name.
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	if !ProcessRunning("sleep") {
		t.Fatalf("ProcessRunning should detect the spawned sleep process")
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
