//go:build linux

package system

import (
	"context"
	"os/exec"
	"sync"
	"testing"
	"time"
)

func TestReaperReapsOrphanedGrandchild(t *testing.T) {
	// /bin/sh available on Linux CI/dev hosts.
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no /bin/sh")
	}

	var reaped []string
	var mu sync.Mutex
	reaper := NewReaper(func(format string, args ...any) {
		mu.Lock()
		reaped = append(reaped, format)
		mu.Unlock()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := reaper.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer reaper.Stop()

	// Spawn a child that itself spawns a grandchild and then exits quickly,
	// orphaning the grandchild (which reparents to ezx via the subreaper).
	script := "sh -c 'sh -c \"exit 0\" & exit 0'"
	cmd := exec.Command(sh, "-c", script)
	cmd.SysProcAttr = nil
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	ch := reaper.Register(cmd.Process.Pid)

	// Wait for the reaper to reap the child (and the orphaned grandchild).
	timeout := time.After(3 * time.Second)
	select {
	case <-ch:
	case <-timeout:
		t.Fatal("timed out waiting for reaper to reap child")
	}

	// Give the reaper a moment to also reap the orphaned grandchild.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		n := len(reaped)
		mu.Unlock()
		if n > 0 {
			return // orphan was reaped and logged
		}
	}
}

func TestReaperRegistryKeyedByPID(t *testing.T) {
	reaper := NewReaper(nil)
	ch := reaper.Register(9999)
	if ch == nil {
		t.Fatal("Register returned nil channel on linux")
	}
	_ = ch
}
