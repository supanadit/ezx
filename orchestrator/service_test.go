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
	start    bool
	exitCode int
	delay    time.Duration
	startC   chan string
	done     chan struct{}
}

func newFakeProc(name string, exitCode int, startC chan string) *fakeProc {
	return &fakeProc{name: name, exitCode: exitCode, startC: startC, done: make(chan struct{})}
}

func (f *fakeProc) Start(_ context.Context, _ []string, _ domain.LogConfig) error {
	f.mu.Lock()
	if f.start {
		f.mu.Unlock()
		return nil
	}
	f.start = true
	f.mu.Unlock()
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
		nil,
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
	// The orchestrator builds a fresh handle per restart, so track how many
	// handles it requests (initial + each restart) via the factory.
	var mu sync.Mutex
	handles := 0
	svc := NewService(
		func(node domain.ProcessNode) process.ProcessRepository {
			mu.Lock()
			handles++
			mu.Unlock()
			return newFakeProc(node.Name, 1, nil)
		},
		&fakeLogger{},
		nil,
	)

	chain := domain.ProcessChain{
		Roots: []domain.ProcessNode{
			{Name: "svc", Restart: &domain.RestartPolicy{Mode: domain.RestartOnFailure, MaxRetries: 1, Backoff: time.Millisecond}},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = svc.Run(ctx, chain)

	mu.Lock()
	defer mu.Unlock()
	if handles < 2 {
		t.Fatalf("handles requested %d times, want at least 2 (initial + 1 restart)", handles)
	}
}

func TestExecNodeRejectsActiveSiblings(t *testing.T) {
	// The orchestrator is sequential, so a prior root's supervise has returned
	// (active=0) by the time an exec root runs. The guard is defensive: it must
	// reject when any process is still being supervised. Simulate that state
	// directly and assert the runNode path errors before exec'ing.
	procs := map[string]*fakeProc{}
	svc := newTestService(procs)
	svc.beginActive() // pretend a sibling is still being supervised

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := svc.Run(ctx, domain.ProcessChain{
		Roots: []domain.ProcessNode{
			{
				Name: "postgres",
				Exec: true,
				Process: domain.Process{
					BinaryPath: "/bin/true",
				},
			},
		},
	})
	if err == nil {
		t.Fatal("expected error for exec node with active sibling, got nil")
	}
}

func TestExecAndHealthConflict(t *testing.T) {
	procs := map[string]*fakeProc{}
	svc := newTestService(procs)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := svc.Run(ctx, domain.ProcessChain{
		Roots: []domain.ProcessNode{
			{
				Name:   "postgres",
				Exec:   true,
				Health: &domain.HealthConfig{},
				Process: domain.Process{
					BinaryPath: "/bin/true",
				},
			},
		},
	})
	if err == nil {
		t.Fatal("expected error for node with both Exec and Health, got nil")
	}
}

func TestLoneLeafRootDefaultsToExecSupervisesOnHealth(t *testing.T) {
	procs := map[string]*fakeProc{"svc": newFakeProc("svc", 0, nil)}
	svc := newTestService(procs)

	// A lone leaf root with Health set must NOT exec — it stays supervised, so
	// the handle is started. (Exec would replace the test process; testing the
	// actual exec is done via the exec integration test.)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := svc.Run(ctx, domain.ProcessChain{
		Roots: []domain.ProcessNode{
			{Name: "svc", Health: &domain.HealthConfig{}},
		},
	})
	if err != nil {
		t.Fatalf("health lone leaf should be supervised, got error: %v", err)
	}
	procs["svc"].mu.Lock()
	started := len(procs["svc"].started)
	procs["svc"].mu.Unlock()
	if started < 1 {
		t.Fatalf("health lone leaf started %d times, want >=1 (supervised, not exec'd)", started)
	}
}
