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
	// The parent must stay "running" (not exit immediately) so the supervised
	// child can start before the parent's supervision ends and drains it.
	parent.delay = 500 * time.Millisecond
	child.delay = 500 * time.Millisecond
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

// TestRunNeedReadyChildrenConcurrent verifies that multiple needParentReady
// children supervise concurrently with each other and with the parent — the
// single-chain sidecar topology (postgres + pgbouncer + sshd + backups).
func TestRunNeedReadyChildrenConcurrent(t *testing.T) {
	startC := make(chan string, 10)
	parent := newFakeProc("parent", 0, startC)
	a := newFakeProc("sidecar-a", 0, startC)
	b := newFakeProc("sidecar-b", 0, startC)
	parent.delay = 400 * time.Millisecond
	a.delay = 400 * time.Millisecond
	b.delay = 400 * time.Millisecond
	procs := map[string]*fakeProc{"parent": parent, "sidecar-a": a, "sidecar-b": b}
	svc := newTestService(procs)

	chain := domain.ProcessChain{
		Roots: []domain.ProcessNode{
			{
				Name: "parent",
				Children: []domain.ProcessNode{
					{Name: "sidecar-a", NeedParentReady: true},
					{Name: "sidecar-b", NeedParentReady: true},
				},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Run(ctx, chain); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// All three must have started before the parent supervision ends.
	// Sequential children would only start one long-running child.
	got := map[string]bool{}
	for i := 0; i < 3; i++ {
		select {
		case n := <-startC:
			got[n] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for starts, got %v", got)
		}
	}
	for _, n := range []string{"parent", "sidecar-a", "sidecar-b"} {
		if !got[n] {
			t.Errorf("process %q never started", n)
		}
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

// waitScheduled polls until the named scheduled node's trigger is registered.
func waitScheduled(t *testing.T, svc *Service, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if svc.Scheduled(name) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("scheduled node %q never registered", name)
}

func TestScheduledNodeManualTriggerFiresTick(t *testing.T) {
	startC := make(chan string, 10)
	procs := map[string]*fakeProc{"job": newFakeProc("job", 0, startC)}
	svc := newTestService(procs)

	chain := domain.ProcessChain{
		Roots: []domain.ProcessNode{
			{
				Name: "job",
				Process: domain.Process{
					BinaryPath: "/bin/true",
				},
				Scheduler: &domain.SchedulerConfig{
					// A yearly cron that never naturally matches during the test.
					Schedule:    domain.CronSchedule{Expression: "0 0 1 1 *"},
					MinInterval: time.Nanosecond,
				},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- svc.Run(ctx, chain) }()

	waitScheduled(t, svc, "job")

	if !svc.Trigger("job") {
		t.Fatal("Trigger(job) returned false, want true (registered and idle)")
	}

	select {
	case n := <-startC:
		if n != "job" {
			t.Fatalf("started %q, want %q", n, "job")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for manual-trigger tick to start")
	}

	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestScheduledNodeUnknownTriggerAndGateSkip(t *testing.T) {
	startC := make(chan string, 10)
	procs := map[string]*fakeProc{"job": newFakeProc("job", 0, startC)}
	svc := newTestService(procs)

	if svc.Trigger("missing") {
		t.Fatal("Trigger(missing) returned true, want false")
	}
	if svc.Scheduled("missing") {
		t.Fatal("Scheduled(missing) returned true, want false")
	}

	// A gate that always fails must skip every tick.
	chain := domain.ProcessChain{
		Roots: []domain.ProcessNode{
			{
				Name: "job",
				Process: domain.Process{
					BinaryPath: "/bin/true",
				},
				Scheduler: &domain.SchedulerConfig{
					Schedule:    domain.CronSchedule{Expression: "0 0 1 1 *"},
					MinInterval: time.Nanosecond,
					Gate: &domain.Probe{
						Type: domain.ProbeTypeExec,
						Exec: []string{"/bin/false"},
					},
				},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- svc.Run(ctx, chain) }()
	waitScheduled(t, svc, "job")

	svc.Trigger("job")
	time.Sleep(50 * time.Millisecond) // give the gate a chance to reject
	select {
	case n := <-startC:
		t.Fatalf("gate should have skipped tick, but %q started", n)
	default:
	}

	cancel()
	<-runDone
}
