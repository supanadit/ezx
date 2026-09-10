package repository

import (
	"context"
	"testing"

	"github.com/supanadit/ezx/domain"
)

// TestCheckProbeAnyComposition verifies the Any (OR) gate composition:
// the gate passes when at least one sub-probe passes.
func TestCheckProbeAnyComposition(t *testing.T) {
	probe := domain.Probe{
		Any: []*domain.Probe{
			{Exec: []string{"/bin/false"}},
			{Exec: []string{"/bin/true"}},
		},
	}
	ok, err := Check(context.Background(), probe)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !ok {
		t.Fatal("Any with one passing sub-probe must pass")
	}

	failing := domain.Probe{
		Any: []*domain.Probe{
			{Exec: []string{"/bin/false"}},
			{Exec: []string{"/bin/false"}},
		},
	}
	ok, err = Check(context.Background(), failing)
	if err == nil {
		t.Fatalf("expected error from failing sub-probes, got ok=%v", ok)
	}
}

// TestCheckProbeAllComposition verifies the All (AND) gate composition: the
// gate fails when any sub-probe fails.
func TestCheckProbeAllComposition(t *testing.T) {
	probe := domain.Probe{
		All: []*domain.Probe{
			{Exec: []string{"/bin/true"}},
			{Exec: []string{"/bin/true"}},
		},
	}
	ok, err := Check(context.Background(), probe)
	if err != nil || !ok {
		t.Fatalf("All with all passing sub-probes must pass (ok=%v err=%v)", ok, err)
	}

	failing := domain.Probe{
		All: []*domain.Probe{
			{Exec: []string{"/bin/true"}},
			{Exec: []string{"/bin/false"}},
		},
	}
	ok, err = Check(context.Background(), failing)
	if err == nil {
		t.Fatal("All with a failing sub-probe must fail")
	}
	if ok {
		t.Fatal("All gate reported ok with a failing sub-probe")
	}
}

// TestCheckProbeExecEnv verifies the exec probe env injection (e.g.
// PGPASSWORD for a psql gate without shell wrappers).
func TestCheckProbeExecEnv(t *testing.T) {
	probe := domain.Probe{
		Exec: []string{"/bin/sh", "-c", `[ "$EZX_PROBE_ENV_TEST" = "hello" ]`},
		Env:  []string{"EZX_PROBE_ENV_TEST=hello"},
	}
	ok, err := Check(context.Background(), probe)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !ok {
		t.Fatal("exec probe with injected env must pass")
	}
}