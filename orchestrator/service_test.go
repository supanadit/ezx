package orchestrator

import (
	"context"
	"errors"
	"os"
	"sync"
	"syscall"
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
	killed   bool
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
func (f *fakeProc) Kill() error {
	f.mu.Lock()
	f.killed = true
	f.mu.Unlock()
	return nil
}
func (f *fakeProc) PID() int               { return 1 }
func (f *fakeProc) Done() <-chan struct{}  { return f.done }
func (f *fakeProc) Output() (string, string) {
	return "", ""
}

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

// TestRunOptionalChildNonFatal verifies that an Optional child that exits
// non-zero does NOT cancel the parent tree and does not surface an error from
// chain.Run. The parent keeps running to completion; the child's failure is
// swallowed (logged only).
func TestRunOptionalChildNonFatal(t *testing.T) {
	startC := make(chan string, 10)
	parent := newFakeProc("parent", 0, startC)
	child := newFakeProc("sidecar", 1, startC)
	parent.delay = 200 * time.Millisecond
	child.delay = 50 * time.Millisecond
	procs := map[string]*fakeProc{"parent": parent, "sidecar": child}
	svc := newTestService(procs)

	chain := domain.ProcessChain{
		Roots: []domain.ProcessNode{
			{
				Name: "parent",
				Children: []domain.ProcessNode{
					{Name: "sidecar", Optional: true},
				},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The optional child exits non-zero but Run must NOT propagate an error.
	if err := svc.Run(ctx, chain); err != nil {
		t.Fatalf("Run returned error for non-fatal optional child: %v", err)
	}
}

// TestRunNonOptionalChildFatal verifies the existing behavior is preserved: a
// child that is NOT Optional and exits non-zero still cancels the tree and
// surfaces an error from chain.Run.
func TestRunNonOptionalChildFatal(t *testing.T) {
	startC := make(chan string, 10)
	parent := newFakeProc("parent", 0, startC)
	child := newFakeProc("child", 5, startC)
	parent.delay = 500 * time.Millisecond
	child.delay = 50 * time.Millisecond
	procs := map[string]*fakeProc{"parent": parent, "child": child}
	svc := newTestService(procs)

	chain := domain.ProcessChain{
		Roots: []domain.ProcessNode{
			{
				Name: "parent",
				Children: []domain.ProcessNode{
					{Name: "child"},
				},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Run(ctx, chain); err == nil {
		t.Fatal("Run returned nil, want *domain.ExitError from non-optional child")
	}
}

// TestRunOptionalChildAfterNeedReady verifies an Optional needParentReady child
// that exits non-zero does not cancel the parent, mirroring the sidecar
// topology (postgres + optional pgbouncer/sshd).
func TestRunOptionalChildAfterNeedReady(t *testing.T) {
	startC := make(chan string, 10)
	parent := newFakeProc("parent", 0, startC)
	child := newFakeProc("sidecar", 3, startC)
	parent.delay = 300 * time.Millisecond
	child.delay = 50 * time.Millisecond
	procs := map[string]*fakeProc{"parent": parent, "sidecar": child}
	svc := newTestService(procs)

	chain := domain.ProcessChain{
		Roots: []domain.ProcessNode{
			{
				Name: "parent",
				Children: []domain.ProcessNode{
					{Name: "sidecar", NeedParentReady: true, Optional: true},
				},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Run(ctx, chain); err != nil {
		t.Fatalf("Run returned error for non-fatal optional child: %v", err)
	}
}

func TestLifecycleCallbacks(t *testing.T) {
	startC := make(chan string, 10)
	var mu sync.Mutex
	onStart := 0
	onExit := []int{}
	procs := map[string]*fakeProc{"svc": newFakeProc("svc", 3, startC)}
	svc := newTestService(procs)

	chain := domain.ProcessChain{
		Roots: []domain.ProcessNode{
			{
				Name: "svc",
				Restart: &domain.RestartPolicy{
					Mode:      domain.RestartNever,
					MaxRetries: 1,
				},
				OnStart: func() {
					mu.Lock()
					onStart++
					mu.Unlock()
				},
				OnExit: func(code int) {
					mu.Lock()
					onExit = append(onExit, code)
					mu.Unlock()
				},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := svc.Run(ctx, chain)
	// The supervised process exits non-zero (3) with RestartNever, so Run must
	// propagate it as an ExitError (the container should exit with that code).
	var ee *domain.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("Run: %v, want *domain.ExitError", err)
	}
	if ee.Code != 3 {
		t.Fatalf("ExitError.Code = %d, want 3", ee.Code)
	}

	mu.Lock()
	defer mu.Unlock()
	if onStart != 1 {
		t.Fatalf("onStart fired %d times, want 1", onStart)
	}
	if len(onExit) != 1 || onExit[0] != 3 {
		t.Fatalf("onExit = %v, want [3]", onExit)
	}
}

func TestScheduledNodeCallbackTick(t *testing.T) {
	procs := map[string]*fakeProc{}
	svc := newTestService(procs)

	var mu sync.Mutex
	ticks := 0
	chain := domain.ProcessChain{
		Roots: []domain.ProcessNode{
			{
				Name:    "job",
				Process: domain.Process{BinaryPath: "/bin/true"},
				Scheduler: &domain.SchedulerConfig{
					Schedule:    domain.CronSchedule{Expression: "0 0 1 1 *"},
					MinInterval: time.Nanosecond,
					Tick: func() {
						mu.Lock()
						ticks++
						mu.Unlock()
					},
				},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- svc.Run(ctx, chain) }()
	waitScheduled(t, svc, "job")

	if !svc.Trigger("job") {
		t.Fatal("Trigger(job) returned false")
	}
	// A callback tick must not spawn a process; wait for it to run.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := ticks
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	mu.Lock()
	if ticks == 0 {
		t.Fatal("callback tick never ran")
	}
	mu.Unlock()

	cancel()
	<-runDone
}

func TestScheduledNodeAutonomousCronTickFires(t *testing.T) {
	procs := map[string]*fakeProc{}
	svc := newTestService(procs)

	var mu sync.Mutex
	ticks := 0
	chain := domain.ProcessChain{
		Roots: []domain.ProcessNode{
			{
				Name:    "job",
				Process: domain.Process{BinaryPath: "/bin/true"},
				Scheduler: &domain.SchedulerConfig{
					Schedule:    domain.CronSchedule{Expression: "* * * * *"},
					MinInterval: time.Nanosecond,
					Tick: func() {
						mu.Lock()
						ticks++
						mu.Unlock()
					},
				},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- svc.Run(ctx, chain) }()
	waitScheduled(t, svc, "job")

	// An autonomous (non-trigger) cron tick fires on the next minute boundary
	// (within <=60s); wait up to ~65s for it.
	deadline := time.Now().Add(65 * time.Second)
	for {
		mu.Lock()
		n := ticks
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("autonomous cron tick never fired")
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	<-runDone
}

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

// TestRunNonZeroExitPropagates verifies that a node with no restart policy
// whose process exits non-zero makes chain.Run return an error that
// errors.As-matches *domain.ExitError with the right code.
func TestRunNonZeroExitPropagates(t *testing.T) {
	procs := map[string]*fakeProc{"svc": newFakeProc("svc", 7, nil)}
	svc := newTestService(procs)

	// A non-nil Restart policy keeps the lone leaf root supervised instead of
	// exec'ing; RestartNever means the process is not restarted on exit.
	chain := domain.ProcessChain{
		Roots: []domain.ProcessNode{{
			Name:    "svc",
			Restart: &domain.RestartPolicy{Mode: domain.RestartNever},
		}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := svc.Run(ctx, chain)
	if err == nil {
		t.Fatal("Run returned nil, want *domain.ExitError")
	}
	var ee *domain.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("Run error %v does not match *domain.ExitError", err)
	}
	if ee.Code != 7 {
		t.Fatalf("ExitError.Code = %d, want 7", ee.Code)
	}
}

// TestRunZeroExitNoError verifies that a process exiting 0 returns nil from
// Run (no ExitError).
func TestRunZeroExitNoError(t *testing.T) {
	procs := map[string]*fakeProc{"svc": newFakeProc("svc", 0, nil)}
	svc := newTestService(procs)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Run(ctx, domain.ProcessChain{
		Roots: []domain.ProcessNode{{
			Name:    "svc",
			Restart: &domain.RestartPolicy{Mode: domain.RestartNever},
		}},
	}); err != nil {
		t.Fatalf("Run returned error for exit-0 process: %v", err)
	}
}

// TestRunReadinessFuncBecomesReady verifies that a node with a ReadinessFunc
// that returns false a few times and then true starts its process and fires
// OnReady once the callback reports ready.
func TestRunReadinessFuncBecomesReady(t *testing.T) {
	startC := make(chan string, 10)
	parent := newFakeProc("parent", 0, startC)
	child := newFakeProc("child", 0, startC)
	parent.delay = 300 * time.Millisecond
	child.delay = 300 * time.Millisecond
	procs := map[string]*fakeProc{"parent": parent, "child": child}
	svc := newTestService(procs)

	var mu sync.Mutex
	readyCalls := 0
	onReady := 0
	chain := domain.ProcessChain{
		Roots: []domain.ProcessNode{{
			Name:     "parent",
			Children: []domain.ProcessNode{{Name: "child", NeedParentReady: true}},
			Readiness: &domain.Probe{Interval: time.Millisecond, MaxAttempts: 100},
			ReadinessFunc: func() bool {
				mu.Lock()
				defer mu.Unlock()
				readyCalls++
				return readyCalls >= 3
			},
			OnReady: func() {
				mu.Lock()
				onReady++
				mu.Unlock()
			},
		}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Run(ctx, chain); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	if onReady != 1 {
		mu.Unlock()
		t.Fatalf("OnReady fired %d times, want 1", onReady)
	}
	mu.Unlock()

	// The child must have started only after the parent became ready.
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

// TestRunReadinessFuncNeverReadySpawnsChildren verifies that a ReadinessFunc
// that always returns false with a bounded MaxAttempts hits the "never became
// ready" path yet still spawns needParentReady children, matching the probe
// path's behavior.
func TestRunReadinessFuncNeverReadySpawnsChildren(t *testing.T) {
	startC := make(chan string, 10)
	parent := newFakeProc("parent", 0, startC)
	child := newFakeProc("child", 0, startC)
	parent.delay = 200 * time.Millisecond
	child.delay = 200 * time.Millisecond
	procs := map[string]*fakeProc{"parent": parent, "child": child}
	svc := newTestService(procs)

	chain := domain.ProcessChain{
		Roots: []domain.ProcessNode{{
			Name:     "parent",
			Children: []domain.ProcessNode{{Name: "child", NeedParentReady: true}},
			Readiness: &domain.Probe{Interval: time.Millisecond, MaxAttempts: 2},
			ReadinessFunc: func() bool {
				return false
			},
		}},
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

// TestDrainNegativeTimeoutUnbounded verifies that a ShutdownConfig with a
// negative Timeout waits indefinitely for the process to exit and never
// force-kills it, even when ForceKill is true.
func TestDrainNegativeTimeoutUnbounded(t *testing.T) {
	// A process that never exits: its Done channel stays open for the duration
	// of the test (simulating postgres ignoring SIGTERM while it flushes WAL).
	proc := newFakeProc("postgres", 0, nil)
	proc.delay = 10 * time.Minute // effectively "keeps running"
	svc := newTestService(map[string]*fakeProc{"postgres": proc})

	cfg := domain.ShutdownConfig{
		Signal:    syscall.SIGTERM,
		Timeout:   -1,
		ForceKill: true,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drained := make(chan error, 1)
	go func() { drained <- svc.drain(ctx, proc, cfg) }()

	// A negative Timeout must skip the force-kill timer: within a short window
	// the process must NOT be killed.
	select {
	case err := <-drained:
		t.Fatalf("drain returned early (%v); negative timeout must wait indefinitely", err)
	case <-time.After(200 * time.Millisecond):
	}

	proc.mu.Lock()
	killed := proc.killed
	proc.mu.Unlock()
	if killed {
		t.Fatal("process was force-killed despite negative (unbounded) timeout")
	}

	// End the wait by letting the process exit; drain must return nil.
	proc.mu.Lock()
	close(proc.done)
	proc.mu.Unlock()
	select {
	case err := <-drained:
		if err != nil {
			t.Fatalf("drain returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("drain did not return after process exited")
	}
}
