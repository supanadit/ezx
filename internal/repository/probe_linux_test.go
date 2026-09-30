//go:build linux

package repository

import (
	"os"
	"os/exec"
	"testing"
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

