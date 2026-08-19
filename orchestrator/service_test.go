package orchestrator

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/logger"
	"github.com/supanadit/ezx/process"
)

// fakeProc implements process.ProcessRepository for testing. It records start
// order and exit codes, and never actually spawns a process.
type fakeProc struct {
	name     string
	mu       sync.Mutex
	started  []string
	exitCode int
	delay    time.Duration
	startC   chan string
	done     chan struct{}
}

func newFakeProc(name string, exitCode int, startC chan string) *fakeProc {
	return &fakeProc{name: name, exitCode: exitCode, startC: startC, done: make(chan struct{})}
}

func (f *fakeProc) Start(_ context.Context, _ []string, _ domain.LogConfig) error {
	if f.startC != nil {
		f.startC <- f.name
	}
	f.mu.Lock()
	f.started = append(f.started, f.name)
	f.mu.Unlock()
	go func() {
		if f.delay > 0 {
			time.Sleep(f.delay)
		}
		close(f.done)
	}()
	return nil
}

func (f *fakeProc) Wait() (int, error)     { <-f.done; return f.exitCode, nil }
func (f *fakeProc) Signal(os.Signal) error { return nil }
func (f *fakeProc) Kill() error            { return nil }
func (f *fakeProc) PID() int               { return 1 }
func (f *fakeProc) Done() <-chan struct{}  { return f.done }

// fakeLogger implements logger.Logger.
type fakeLogger struct{}

func (f *fakeLogger) Debug(string, ...any)      {}
func (f *fakeLogger) Info(string, ...any)       {}
func (f *fakeLogger) Warn(string, ...any)       {}
func (f *fakeLogger) Error(string, ...any)      {}
func (f *fakeLogger) Enabled(logger.Level) bool { return false }

// newTestService wires fakes into a Service.
func newTestService(procs map[string]*fakeProc) *Service {
	return NewService(
		func(node domain.ProcessNode) process.ProcessRepository {
			return procs[node.Name]
		},
		&fakeLogger{},
	)
}

var _ logger.Logger = (*fakeLogger)(nil)
var _ process.ProcessRepository = (*fakeProc)(nil)

func TestRunStartsNeedReadyChildAfterParent(t *testing.T) {
	startC := make(chan string, 10)
	parent := newFakeProc("parent", 0, startC)
	child := newFakeProc("child", 0, startC)
	procs := map[string]*fakeProc{"parent": parent, "child": child}
	svc := newTestService(procs)

	chain := domain.ProcessChain{
		Roots: []domain.ProcessNode{
			{Name: "parent", Children: []domain.ProcessNode{{Name: "child", NeedParentReady: true}}},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Run(ctx, chain); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var order []string
	for i := 0; i < 2; i++ {
		select {
		case n := <-startC:
			order = append(order, n)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for starts")
		}
	}
	if len(order) != 2 || order[0] != "parent" || order[1] != "child" {
		t.Fatalf("start order = %v, want [parent child]", order)
	}
}

func TestRunRestartsOnFailure(t *testing.T) {
	proc := newFakeProc("svc", 1, nil)
	procs := map[string]*fakeProc{"svc": proc}
	svc := NewService(
		func(node domain.ProcessNode) process.ProcessRepository { return procs[node.Name] },
		&fakeLogger{},
	)

	chain := domain.ProcessChain{
		Roots: []domain.ProcessNode{
			{Name: "svc", Restart: &domain.RestartPolicy{Mode: domain.RestartOnFailure, MaxRetries: 1, Backoff: time.Millisecond}},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = svc.Run(ctx, chain)

	proc.mu.Lock()
	defer proc.mu.Unlock()
	if len(proc.started) < 2 {
		t.Fatalf("started %d times, want at least 2 (initial + 1 restart)", len(proc.started))
	}
}
