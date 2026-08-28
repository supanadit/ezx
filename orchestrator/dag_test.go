package orchestrator

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/process"
)

// failingStartProc is a fake process whose Start always fails — used to make a
// node fail before it signals started/ready, so its dependents get skipped.
type failingStartProc struct {
	name string
}

func (f *failingStartProc) Start(_ context.Context, _ []string, _ domain.LogConfig) error {
	return errors.New("start failed")
}
func (f *failingStartProc) Wait() (int, error)             { return 0, nil }
func (f *failingStartProc) Signal(os.Signal) error         { return nil }
func (f *failingStartProc) Kill() error                    { return nil }
func (f *failingStartProc) PID() int                       { return 0 }
func (f *failingStartProc) Done() <-chan struct{}          { return make(chan struct{}) }
func (f *failingStartProc) Output() (string, string)       { return "", "" }

// blockingProc is a fake process that starts (recording to startC) and then
// blocks forever until killed — used to model a long-running dep that never
// exits on its own.
type blockingProc struct {
	name   string
	startC chan string
	done   chan struct{}
}

func (p *blockingProc) Start(_ context.Context, _ []string, _ domain.LogConfig) error {
	if p.startC != nil {
		p.startC <- p.name
	}
	return nil
}
func (p *blockingProc) Wait() (int, error)       { <-p.done; return 0, nil }
func (p *blockingProc) Signal(os.Signal) error   { return nil }
func (p *blockingProc) Kill() error              { close(p.done); return nil }
func (p *blockingProc) PID() int                 { return 1 }
func (p *blockingProc) Done() <-chan struct{}    { return p.done }
func (p *blockingProc) Output() (string, string) { return "", "" }

// TestDAGFanIn verifies fan-in: a node that depends on two parents (both
// needParentReady) starts only after BOTH are ready — not after the first.
func TestDAGFanIn(t *testing.T) {
	startC := make(chan string, 10)
	a := newFakeProc("a", 0, startC)
	b := newFakeProc("b", 0, startC)
	c := newFakeProc("c", 0, startC)
	a.delay = 300 * time.Millisecond
	b.delay = 300 * time.Millisecond
	c.delay = 300 * time.Millisecond
	procs := map[string]*fakeProc{"a": a, "b": b, "c": c}
	svc := newTestService(procs)

	var mu sync.Mutex
	aCalls, bCalls := 0, 0
	chain := domain.ProcessChain{Nodes: []domain.ProcessNode{
		{Name: "a",
			Readiness:     &domain.Probe{Interval: time.Millisecond, MaxAttempts: 100},
			ReadinessFunc: func() bool { mu.Lock(); aCalls++; n := aCalls; mu.Unlock(); return n >= 2 }},
		{Name: "b",
			Readiness:     &domain.Probe{Interval: time.Millisecond, MaxAttempts: 100},
			ReadinessFunc: func() bool { mu.Lock(); bCalls++; n := bCalls; mu.Unlock(); return n >= 4 }},
		{Name: "c", DependsOn: []string{"a", "b"}, NeedParentReady: true},
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Run(ctx, chain); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var started []string
	for i := 0; i < 3; i++ {
		select {
		case n := <-startC:
			started = append(started, n)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for starts, got %v", started)
		}
	}
	// c waits on BOTH a (ready ~2ms) and b (ready ~4ms), so c must be last.
	if started[2] != "c" {
		t.Fatalf("fan-in: c must start last (after a and b ready), got %v", started)
	}
}

// TestDAGSharedDependency verifies shared-dependency reverse-lifetime: A is a
// shared dep of B and C. B exits early and cleanly (siblings and A unaffected);
// when A exits permanently, its running dependent C is drained. B's early exit
// must not fail the chain nor kill A or C.
func TestDAGSharedDependency(t *testing.T) {
	startC := make(chan string, 10)
	a := newFakeProc("a", 0, startC)
	b := newFakeProc("b", 0, startC)
	c := newFakeProc("c", 0, startC)
	a.delay = 150 * time.Millisecond
	b.delay = 30 * time.Millisecond
	c.delay = 500 * time.Millisecond
	procs := map[string]*fakeProc{"a": a, "b": b, "c": c}
	svc := newTestService(procs)

	chain := domain.ProcessChain{Nodes: []domain.ProcessNode{
		{Name: "a"},
		{Name: "b", DependsOn: []string{"a"}},
		{Name: "c", DependsOn: []string{"a"},
			Shutdown: &domain.ShutdownConfig{Signal: syscall.SIGTERM, Timeout: 5 * time.Millisecond, ForceKill: true}},
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Run(ctx, chain); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// C outlives A (500ms vs 150ms), so when A exits C must be drained
	// (force-killed by its short-drain shutdown config).
	if !c.killed {
		t.Fatal("C should have been force-killed when its shared dependency A exited")
	}
	// A and B exit cleanly on their own (B before A); neither is drained/killed.
	if a.killed {
		t.Fatal("A should exit cleanly, not be killed")
	}
	if b.killed {
		t.Fatal("B should exit cleanly, not be killed by its sibling's/other node's exit")
	}
}

// TestDAGDiamond verifies the diamond topology A→(B,C)→D with needParentReady
// on every edge: D starts only after both B and C are ready.
func TestDAGDiamond(t *testing.T) {
	startC := make(chan string, 10)
	a := newFakeProc("a", 0, startC)
	b := newFakeProc("b", 0, startC)
	c := newFakeProc("c", 0, startC)
	d := newFakeProc("d", 0, startC)
	a.delay = 300 * time.Millisecond
	b.delay = 300 * time.Millisecond
	c.delay = 300 * time.Millisecond
	d.delay = 300 * time.Millisecond
	procs := map[string]*fakeProc{"a": a, "b": b, "c": c, "d": d}
	svc := newTestService(procs)

	chain := domain.ProcessChain{Nodes: []domain.ProcessNode{
		{Name: "a"},
		{Name: "b", DependsOn: []string{"a"}, NeedParentReady: true},
		{Name: "c", DependsOn: []string{"a"}, NeedParentReady: true},
		{Name: "d", DependsOn: []string{"b", "c"}, NeedParentReady: true},
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Run(ctx, chain); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var started []string
	for i := 0; i < 4; i++ {
		select {
		case n := <-startC:
			started = append(started, n)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for starts, got %v", started)
		}
	}
	if started[0] != "a" {
		t.Fatalf("diamond: a must start first, got %v", started)
	}
	if started[3] != "d" {
		t.Fatalf("diamond: d must start last (after both b and c ready), got %v", started)
	}
}

// TestDAGOptionalDepFailureSkipsDependents verifies an Optional node that fails
// at start (before becoming ready) causes its dependents to be skipped and the
// chain to continue (Run returns nil).
func TestDAGOptionalDepFailureSkipsDependents(t *testing.T) {
	startC := make(chan string, 10)
	svc := NewService(
		func(node domain.ProcessNode) process.ProcessRepository {
			if node.Name == "b" {
				return &failingStartProc{name: node.Name}
			}
			return newFakeProc(node.Name, 0, startC)
		},
		&fakeLogger{},
		nil,
	)

	chain := domain.ProcessChain{Nodes: []domain.ProcessNode{
		{Name: "a"},
		{Name: "b", Optional: true, DependsOn: []string{"a"}},
		{Name: "c", DependsOn: []string{"b"}, NeedParentReady: true},
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Run(ctx, chain); err != nil {
		t.Fatalf("Run returned error for optional failure: %v", err)
	}

	// Only a starts; b fails at start (optional) and c is skipped.
	select {
	case n := <-startC:
		if n != "a" {
			t.Fatalf("first start = %q, want a", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a to start")
	}
	select {
	case n := <-startC:
		t.Fatalf("c should be skipped after optional b failed, but %q started", n)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestDAGFatalFailureFailFast verifies a fatal (non-Optional) node failure
// fails the whole chain fast and propagates its ExitError.
func TestDAGFatalFailureFailFast(t *testing.T) {
	startC := make(chan string, 10)
	a := newFakeProc("a", 0, startC)
	b := newFakeProc("b", 5, startC)
	a.delay = 500 * time.Millisecond
	b.delay = 30 * time.Millisecond
	procs := map[string]*fakeProc{"a": a, "b": b}
	svc := newTestService(procs)

	chain := domain.ProcessChain{Nodes: []domain.ProcessNode{
		{Name: "a"},
		{Name: "b", DependsOn: []string{"a"}},
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := svc.Run(ctx, chain)
	var ee *domain.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("Run error = %v, want *domain.ExitError", err)
	}
	if ee.Code != 5 {
		t.Fatalf("ExitError.Code = %d, want 5", ee.Code)
	}
}

// TestDAGMultiRootConcurrent verifies D7: multi-root (no-dependency) nodes start
// concurrently, not sequentially.
func TestDAGMultiRootConcurrent(t *testing.T) {
	startC := make(chan string, 10)
	a := newFakeProc("a", 0, startC)
	b := newFakeProc("b", 0, startC)
	// Long-lived: a sequential runner would block on the first and never start
	// the second; concurrent starts both immediately.
	a.delay = 10 * time.Minute
	b.delay = 10 * time.Minute
	procs := map[string]*fakeProc{"a": a, "b": b}
	svc := newTestService(procs)

	chain := domain.ProcessChain{Nodes: []domain.ProcessNode{
		{Name: "a", Shutdown: &domain.ShutdownConfig{Signal: syscall.SIGTERM, Timeout: 5 * time.Millisecond, ForceKill: true}},
		{Name: "b", Shutdown: &domain.ShutdownConfig{Signal: syscall.SIGTERM, Timeout: 5 * time.Millisecond, ForceKill: true}},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- svc.Run(ctx, chain) }()

	var started []string
	for i := 0; i < 2; i++ {
		select {
		case n := <-startC:
			started = append(started, n)
		case <-time.After(2 * time.Second):
			t.Fatalf("multi-root: only %v started; roots must start concurrently", started)
		}
	}
	cancel()
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// TestDAGRunRejectsExecMisconfig verifies the orchestrator surfaces exec
// restriction validation errors (exec + DependsOn, and a node depending on an
// exec node).
func TestDAGRunRejectsExecMisconfig(t *testing.T) {
	svc := newTestService(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := svc.Run(ctx, domain.ProcessChain{Nodes: []domain.ProcessNode{
		{Name: "a"},
		{Name: "b", Exec: true, DependsOn: []string{"a"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "may only depend on oneshot nodes") {
		t.Fatalf("exec+non-oneshot-DependsOn: Run error = %v, want exec restriction error", err)
	}

	err = svc.Run(ctx, domain.ProcessChain{Nodes: []domain.ProcessNode{
		{Name: "a", Exec: true},
		{Name: "b", DependsOn: []string{"a"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "depends on exec node") {
		t.Fatalf("dependent-on-exec: Run error = %v, want exec restriction error", err)
	}
}

// TestDAGTreeDesugarEquivalent verifies the desugar compatibility guarantee: a
// legacy children tree and its flattened dependsOn form produce identical
// orchestration (start order).
func TestDAGTreeDesugarEquivalent(t *testing.T) {
	run := func(chain domain.ProcessChain) []string {
		startC := make(chan string, 10)
		postgres := newFakeProc("postgres", 0, startC)
		sidecar := newFakeProc("sidecar", 0, startC)
		postgres.delay = 200 * time.Millisecond
		sidecar.delay = 200 * time.Millisecond
		svc := newTestService(map[string]*fakeProc{"postgres": postgres, "sidecar": sidecar})
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
				t.Fatalf("timed out waiting for starts, got %v", order)
			}
		}
		return order
	}

	tree := domain.ProcessChain{Roots: []domain.ProcessNode{{
		Name:     "postgres",
		Children: []domain.ProcessNode{{Name: "sidecar", NeedParentReady: true}},
	}}}
	flat := domain.ProcessChain{Nodes: []domain.ProcessNode{
		{Name: "postgres"},
		{Name: "sidecar", NeedParentReady: true, DependsOn: []string{"postgres"}},
	}}

	orderTree := run(tree)
	orderFlat := run(flat)
	if len(orderTree) != 2 || orderTree[0] != "postgres" || orderTree[1] != "sidecar" {
		t.Fatalf("tree form start order = %v, want [postgres sidecar]", orderTree)
	}
	if len(orderFlat) != 2 || orderFlat[0] != "postgres" || orderFlat[1] != "sidecar" {
		t.Fatalf("flat form start order = %v, want [postgres sidecar]", orderFlat)
	}
	if orderTree[0] != orderFlat[0] || orderTree[1] != orderFlat[1] {
		t.Fatalf("desugar mismatch: tree %v vs flat %v", orderTree, orderFlat)
	}
}

// TestDAGMixedEdgeWaitModes verifies per-edge wait modes: a dependent that
// waits for A to be ready but only for B to start starts only after BOTH
// conditions are met (A ready, B started).
func TestDAGMixedEdgeWaitModes(t *testing.T) {
	startC := make(chan string, 10)
	a := &blockingProc{name: "a", startC: startC, done: make(chan struct{})}
	b := &blockingProc{name: "b", startC: startC, done: make(chan struct{})}
	c := &blockingProc{name: "c", startC: startC, done: make(chan struct{})}
	svc := NewService(
		func(node domain.ProcessNode) process.ProcessRepository {
			switch node.Name {
			case "a":
				return a
			case "b":
				return b
			default:
				return c
			}
		},
		&fakeLogger{},
		nil,
	)
	shutdown := &domain.ShutdownConfig{Signal: syscall.SIGTERM, Timeout: 5 * time.Millisecond, ForceKill: true}

	chain := domain.ProcessChain{Nodes: []domain.ProcessNode{
		{Name: "a", Shutdown: shutdown,
			Readiness:     &domain.Probe{Interval: time.Millisecond, MaxAttempts: 100},
			ReadinessFunc: func() bool { return true }},
		{Name: "b", Shutdown: shutdown},
		{Name: "c", Shutdown: shutdown, DependsOnEdges: []domain.Dependency{
			{Name: "a", WaitFor: domain.WaitReady},   // wait for a's readiness
			{Name: "b", WaitFor: domain.WaitStarted}, // only wait for b to start
		}},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- svc.Run(ctx, chain) }()

	var started []string
	for i := 0; i < 3; i++ {
		select {
		case n := <-startC:
			started = append(started, n)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for starts, got %v", started)
		}
	}
	// c waits on a (ready) AND b (started); both roots start together, so c must
	// be last.
	if started[2] != "c" {
		t.Fatalf("mixed edges: c must start last (after a ready and b started), got %v", started)
	}
	cancel()
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// TestDAGExitEdgeOneshotSuccess verifies an `exit` edge: a dependent that waits
// for a oneshot dep to exit starts only after the oneshot exits 0.
func TestDAGExitEdgeOneshotSuccess(t *testing.T) {
	startC := make(chan string, 10)
	init := newFakeProc("init", 0, startC) // exit code 0
	app := newFakeProc("app", 0, startC)
	procs := map[string]*fakeProc{"init": init, "app": app}
	svc := newTestService(procs)

	chain := domain.ProcessChain{Nodes: []domain.ProcessNode{
		{Name: "init", Oneshot: true},
		{Name: "app", DependsOnEdges: []domain.Dependency{{Name: "init", WaitFor: domain.WaitExit}}},
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Run(ctx, chain); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var started []string
	for i := 0; i < 2; i++ {
		select {
		case n := <-startC:
			started = append(started, n)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for starts, got %v", started)
		}
	}
	if len(started) != 2 || started[0] != "init" || started[1] != "app" {
		t.Fatalf("exit-edge oneshot start order = %v, want [init app]", started)
	}
}

// TestDAGExitEdgeOneshotFailureSkipsDependent verifies an `exit` edge where the
// oneshot dep exits non-zero: the optional dep fails non-fatally and the
// dependent is skipped (never starts).
func TestDAGExitEdgeOneshotFailureSkipsDependent(t *testing.T) {
	startC := make(chan string, 10)
	init := newFakeProc("init", 5, startC) // exit code 5 (failure)
	app := newFakeProc("app", 0, startC)
	procs := map[string]*fakeProc{"init": init, "app": app}
	svc := newTestService(procs)

	chain := domain.ProcessChain{Nodes: []domain.ProcessNode{
		{Name: "init", Oneshot: true, Optional: true},
		{Name: "app", DependsOnEdges: []domain.Dependency{{Name: "init", WaitFor: domain.WaitExit}}},
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Run(ctx, chain); err != nil {
		t.Fatalf("Run returned error for optional oneshot failure: %v", err)
	}

	select {
	case n := <-startC:
		if n != "init" {
			t.Fatalf("first start = %q, want init", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for init to start")
	}
	select {
	case n := <-startC:
		t.Fatalf("app should be skipped after failed oneshot, but %q started", n)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestDAGExitEdgeLongRunningNeverExits verifies an `exit` edge on a long-running
// dep that never exits: the dependent must NOT start while the dep is alive.
func TestDAGExitEdgeLongRunningNeverExits(t *testing.T) {
	startC := make(chan string, 10)
	blocking := &blockingProc{name: "svc", startC: startC, done: make(chan struct{})}
	app := newFakeProc("app", 0, startC)
	svc := NewService(
		func(node domain.ProcessNode) process.ProcessRepository {
			if node.Name == "svc" {
				return blocking
			}
			return app
		},
		&fakeLogger{},
		nil,
	)

	chain := domain.ProcessChain{Nodes: []domain.ProcessNode{
		{Name: "svc",
			Shutdown: &domain.ShutdownConfig{Signal: syscall.SIGTERM, Timeout: 5 * time.Millisecond, ForceKill: true}},
		{Name: "app", DependsOnEdges: []domain.Dependency{{Name: "svc", WaitFor: domain.WaitExit}}},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- svc.Run(ctx, chain) }()

	// svc starts; app must NOT start while svc (an exit-edge dep) never exits.
	select {
	case n := <-startC:
		if n != "svc" {
			t.Fatalf("first start = %q, want svc", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for svc to start")
	}
	select {
	case n := <-startC:
		t.Fatalf("app must not start while exit-edge dep svc is alive, but %q started", n)
	case <-time.After(200 * time.Millisecond):
	}

	// Cancelling drains svc and lets Run return.
	cancel()
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// TestDAGLegacyEqualsPerEdgeReady verifies the backward-compat equivalence:
// dependsOn:["a"] + needParentReady:true ≡ dependsOnEdges:[{name:"a",waitFor:"ready"}],
// producing identical start order.
func TestDAGLegacyEqualsPerEdgeReady(t *testing.T) {
	run := func(chain domain.ProcessChain) []string {
		startC := make(chan string, 10)
		a := newFakeProc("a", 0, startC)
		b := newFakeProc("b", 0, startC)
		a.delay = 200 * time.Millisecond
		b.delay = 200 * time.Millisecond
		procs := map[string]*fakeProc{"a": a, "b": b}
		svc := newTestService(procs)
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
				t.Fatalf("timed out waiting for starts, got %v", order)
			}
		}
		return order
	}

	legacy := domain.ProcessChain{Nodes: []domain.ProcessNode{
		{Name: "a"},
		{Name: "b", DependsOn: []string{"a"}, NeedParentReady: true},
	}}
	perEdge := domain.ProcessChain{Nodes: []domain.ProcessNode{
		{Name: "a"},
		{Name: "b", DependsOnEdges: []domain.Dependency{{Name: "a", WaitFor: domain.WaitReady}}},
	}}
	legacyOrder := run(legacy)
	perEdgeOrder := run(perEdge)
	if !reflect.DeepEqual(legacyOrder, perEdgeOrder) {
		t.Fatalf("legacy needParentReady order %v != dependsOnEdges ready order %v", legacyOrder, perEdgeOrder)
	}
	if len(legacyOrder) != 2 || legacyOrder[0] != "a" || legacyOrder[1] != "b" {
		t.Fatalf("legacy order = %v, want [a b]", legacyOrder)
	}
}
