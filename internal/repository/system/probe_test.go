//go:build linux

package system

import (
	"context"
	"testing"

	"github.com/supanadit/ezx/domain"
)

// TestExecExpect verifies that an exec probe gates on stdout content via
// ExecExpect / ExecExpectExact.
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
		Type:            domain.ProbeTypeExec,
		Exec:            []string{"/bin/sh", "-c", `printf " f "`},
		ExecExpect:      "f",
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
