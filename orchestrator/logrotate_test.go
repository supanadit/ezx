package orchestrator

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/rotator"
)

// fakeRotator records the config it was started with and blocks until its
// context is cancelled, so the test can assert the session was started and
// still observe its target.
type fakeRotator struct {
	called     chan domain.LogRotateConfig
	target     rotator.Target
	pidAtStart int
}

func (f *fakeRotator) Run(ctx context.Context, cfg domain.LogRotateConfig, target rotator.Target) error {
	f.target = target
	f.pidAtStart = target.PID()
	f.called <- cfg
	<-ctx.Done()
	return nil
}

// TestLogRotateSessionStarted verifies a node with logRotate starts a rotation
// session bound to its supervised process, and that the target resolves to the
// running PID and forwards signals.
func TestLogRotateSessionStarted(t *testing.T) {
	procs := map[string]*fakeProc{"app": newFakeProc("app", 0, nil)}
	procs["app"].delay = 200 * time.Millisecond
	svc := newTestService(procs)
	fr := &fakeRotator{called: make(chan domain.LogRotateConfig, 1)}
	svc.SetRotator(func() rotator.Rotator { return fr })

	noExec := false
	chain := domain.ProcessChain{
		ExecDefault: &noExec,
		Nodes: []domain.ProcessNode{{
			Name:      "app",
			Process:   domain.Process{BinaryPath: "/bin/true"},
			LogRotate: &domain.LogRotateConfig{FilePath: "/tmp/ezx-test.log", MaxBytes: 1, Signal: "USR1"},
		}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Run(ctx, chain); err != nil {
		t.Fatalf("Run: %v", err)
	}

	select {
	case cfg := <-fr.called:
		if cfg.Signal != "USR1" {
			t.Fatalf("rotation config signal = %q, want USR1", cfg.Signal)
		}
	default:
		t.Fatalf("rotation session was not started")
	}
	if fr.target == nil {
		t.Fatalf("rotation target was not set")
	}
	if fr.pidAtStart != 1 {
		t.Fatalf("target PID at start = %d, want 1", fr.pidAtStart)
	}
	if err := fr.target.Signal(syscall.SIGUSR1); err != nil {
		t.Fatalf("target.Signal: %v", err)
	}
}

// TestNoLogRotateSessionWithoutConfig verifies a node without logRotate does not
// start a rotation session.
func TestNoLogRotateSessionWithoutConfig(t *testing.T) {
	procs := map[string]*fakeProc{"app": newFakeProc("app", 0, nil)}
	procs["app"].delay = 50 * time.Millisecond
	svc := newTestService(procs)
	fr := &fakeRotator{called: make(chan domain.LogRotateConfig, 1)}
	svc.SetRotator(func() rotator.Rotator { return fr })

	noExec := false
	chain := domain.ProcessChain{
		ExecDefault: &noExec,
		Nodes:       []domain.ProcessNode{{Name: "app", Process: domain.Process{BinaryPath: "/bin/true"}}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Run(ctx, chain); err != nil {
		t.Fatalf("Run: %v", err)
	}
	select {
	case cfg := <-fr.called:
		t.Fatalf("unexpected rotation session for config %+v", cfg)
	default:
	}
}
