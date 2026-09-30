package js

import (
	"context"
	"testing"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/internal/repository/system"
	"github.com/supanadit/ezx/internal/script"
	"github.com/supanadit/ezx/orchestrator"
	"github.com/supanadit/ezx/process"
	"github.com/supanadit/ezx/runtime"
)

// TestParallelNodeCallbacksAreSerialized is the regression test for the VM
// data race: several independent nodes start and exit concurrently, each firing
// onStart and onExit into the same single-threaded goja runtime. Every callback
// must reach the VM, and none may overlap with another or with the script
// frame.
//
// The assertions run inside the script after chain.run returns, so a callback
// that was silently skipped fails the test rather than passing as "no races".
// Run with -race to catch a regression in the gate.
func TestParallelNodeCallbacksAreSerialized(t *testing.T) {
	const nodes = 8

	log := system.NewLogger()
	factory := func(node domain.ProcessNode) process.ProcessRepository {
		return system.NewProcessRepository(node, nil)
	}
	orch := orchestrator.NewService(factory, log, nil, testOrchestratorDeps())

	reg := runtime.NewRegistry()
	reg.Register("ezx", func(b runtime.Binder) any {
		return script.NewEzxModule(script.Deps{
			Ctx:       context.Background(),
			Log:       log,
			Proc:      factory,
			Chain:     orch,
			Callbacks: b.Invoker(),
			Gate:      b.Gate(),
		})
	})
	engine := NewEngine(reg)

	// Eight independent nodes (no dependsOn) start together and each run its
	// process to completion, so onStart and onExit both fire from node
	// goroutines while the script is parked inside chain.run.
	src := `
		const { chain } = require("ezx");
		let starts = 0;
		let exits = 0;
		let maxConcurrent = 0;
		let live = 0;
		const nodes = [];
		for (let i = 0; i < ` + itoa(nodes) + `; i++) {
			nodes.push({
				name: "n" + i,
				process: { binaryPath: "/bin/sleep", arguments: ["0.2"] },
				onStart: () => {
					live++;
					if (live > maxConcurrent) maxConcurrent = live;
					starts++;
				},
				onExit: (code) => {
					live--;
					exits++;
				},
			});
		}
		chain.run({ nodes: nodes });
		if (starts !== ` + itoa(nodes) + `) throw new Error("onStart fired " + starts + " times, want ` + itoa(nodes) + `");
		if (exits !== ` + itoa(nodes) + `) throw new Error("onExit fired " + exits + " times, want ` + itoa(nodes) + `");
		if (maxConcurrent !== ` + itoa(nodes) + `) throw new Error("max concurrent callbacks = " + maxConcurrent + ", want ` + itoa(nodes) + `");
	`
	if err := engine.RunString(context.Background(), src); err != nil {
		t.Fatalf("RunString: %v", err)
	}
}

// itoa formats a small non-negative int for embedding in a script literal.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
