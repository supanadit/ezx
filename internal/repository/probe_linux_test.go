//go:build linux

package repository

import (
	"os"
	"testing"
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
