package js

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/internal/repository/system"
	"github.com/supanadit/ezx/internal/script"
	"github.com/supanadit/ezx/logger"
	"github.com/supanadit/ezx/orchestrator"
	"github.com/supanadit/ezx/process"
	"github.com/supanadit/ezx/runtime"
)

// registerTestHostModule wires the aggregate ezx host module into reg using
// the new Deps API; b.Invoker() provides callback support to ezx.api.
func registerTestHostModule(reg *runtime.Registry, ctx context.Context, log logger.Logger, factory script.ProcessFactory, orch *orchestrator.Service, router *echo.Echo) {
	reg.Register("ezx", func(b runtime.Binder) any {
		return script.NewEzxModule(script.Deps{
			Ctx:       ctx,
			Log:       log,
			Proc:      factory,
			Chain:     orch,
			Sched:     orch,
			Routes:    router,
			Callbacks: b.Invoker(),
		})
	})
}

// buildTestEngine wires an Engine with the env + editor host modules and a
// capturing logger, for exercising host APIs from JS.
func buildTestEngine(t *testing.T) (*Engine, *bytes.Buffer) {
	t.Helper()
	reg := runtime.NewRegistry()
	var buf bytes.Buffer
	log := system.NewLogger()
	log.SetOutput(&buf)
	registerTestHostModule(reg, context.Background(), log, func(node domain.ProcessNode) process.ProcessRepository {
		return system.NewProcessRepository(node, nil)
	}, nil, nil)
	return NewEngine(reg), &buf
}

func TestScriptEnvModule(t *testing.T) {
	t.Setenv("EZX_TEST_VAR", "hello")
	engine, _ := buildTestEngine(t)

	src := `
		const { env } = require("ezx");
		if (env.get("EZX_TEST_VAR") !== "hello") throw new Error("get failed");
		if (!env.has("EZX_TEST_VAR")) throw new Error("has failed");
		if (env.has("EZX_MISSING")) throw new Error("has should be false");
		if (env.get("EZX_MISSING", "def") !== "def") throw new Error("default failed");
	`
	if err := engine.RunString(context.Background(), src); err != nil {
		t.Fatalf("RunString: %v", err)
	}
}

func TestScriptEditorModule(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "app.ini")
	engine, _ := buildTestEngine(t)

	src := `
		const { editor } = require("ezx");
		const e = editor.open(%q);
		e.upsert(/^\\s*memory_limit\\s*=/, "memory_limit = 512M");
	`
	src = strings.ReplaceAll(src, "%q", strconv.Quote(target))
	if err := engine.RunString(context.Background(), src); err != nil {
		t.Fatalf("RunString: %v", err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read %s: %v", target, err)
	}
	if !strings.Contains(string(data), "memory_limit = 512M") {
		t.Fatalf("file content = %q, want it to contain 'memory_limit = 512M'", string(data))
	}
}

func TestScriptFSModule(t *testing.T) {
	parent := t.TempDir()
	sub := filepath.Join(parent, "subdir")
	engine, _ := buildTestEngine(t)

	src := fmt.Sprintf(`
		const { fs } = require("ezx");
		if (fs.exists(%q)) throw new Error("exists should be false");
		fs.mkdirAll(%q, 0o755);
		if (!fs.exists(%q)) throw new Error("exists should be true after mkdir");
		const names = fs.readDir(%q);
		if (names.length !== 1 || names[0] !== "subdir") throw new Error("readDir wrong: " + names.join(","));
	`, sub, sub, sub, parent)
	if err := engine.RunString(context.Background(), src); err != nil {
		t.Fatalf("RunString: %v", err)
	}
}

func TestScriptProcessModule(t *testing.T) {
	engine, _ := buildTestEngine(t)

	src := `
		const { process } = require("ezx");
		const p = process.spawn({
			name: "echo",
			process: { binaryPath: "/bin/sh", arguments: ["-c", "exit 3"] },
		});
		p.start([]);
		const code = p.wait();
		if (code !== 3) throw new Error("expected exit 3, got " + code);
	`
	if err := engine.RunString(context.Background(), src); err != nil {
		t.Fatalf("RunString: %v", err)
	}
}

// buildTestEngineWithRouter wires an Engine with the env + editor host
// modules, a shared echo router (for ezx.api), and a capturing logger.
func buildTestEngineWithRouter(t *testing.T, router *echo.Echo) (*Engine, *bytes.Buffer) {
	t.Helper()
	reg := runtime.NewRegistry()
	var buf bytes.Buffer
	log := system.NewLogger()
	log.SetOutput(&buf)
	registerTestHostModule(reg, context.Background(), log, func(node domain.ProcessNode) process.ProcessRepository {
		return system.NewProcessRepository(node, nil)
	}, nil, router)
	return NewEngine(reg), &buf
}

func TestScriptSchedulerBuildAndAPI(t *testing.T) {
	router := echo.New()
	engine, _ := buildTestEngineWithRouter(t, router)

	src := `
		const { scheduler, api } = require("ezx");
		// scheduler.build validates a valid cron and returns the config (no throw).
		scheduler.build({
			schedule: { expression: "20 2,8,14,20 * * *", timezone: "UTC" },
			initialDelay: 120e9,
			minInterval: 60e9,
			gate: { type: "exec", exec: ["/bin/true"] },
		});
		// invalid cron must throw.
		let threw = false;
		try { scheduler.build({ schedule: { expression: "not a cron" } }); } catch (e) { threw = true; }
		if (!threw) throw new Error("invalid cron should throw");
		// scheduler.parse round-trips a valid expression.
		if (scheduler.parse("*/15 * * * *") !== "*/15 * * * *") throw new Error("parse failed");
		// Register a user route; the handler's return is JSON-encoded.
		api.post("/backup/full", () => ({ ok: true, type: "full" }));
	`
	if err := engine.RunString(context.Background(), src); err != nil {
		t.Fatalf("RunString: %v", err)
	}

	// Hit the registered route and assert JSON response.
	req := httptest.NewRequest(http.MethodPost, "/backup/full", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /backup/full code = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("body = %q, want ok:true", rec.Body.String())
	}
}

func TestScriptYamlBuild(t *testing.T) {
	engine, _ := buildTestEngine(t)

	src := `
		const { yaml } = require("ezx");
		const doc = yaml.build({
			scope: "postgres-cluster",
			restapi: { listen: "0.0.0.0:8008" },
			etcd3: { hosts: ["a:2379", "b:2379"] },
			bootstrap: { dcs: { ttl: 30, loop_wait: 10 } },
			postgresql: { parameters: { hot_standby: "on", port: 5432 } },
		});
		if (!doc.includes("scope: postgres-cluster")) throw new Error("scope missing");
		if (!doc.includes("hot_standby: \"on\"")) throw new Error("hot_standby not quoted: " + doc);
		if (!doc.includes("port: 5432")) throw new Error("number not bare");
		if (!doc.includes("ttl: 30")) throw new Error("ttl missing");
		if (!doc.includes("hosts:")) throw new Error("hosts missing");
	`
	if err := engine.RunString(context.Background(), src); err != nil {
		t.Fatalf("RunString: %v", err)
	}
}

// e2eFakeProc is a process.ProcessRepository that records starts to a channel
// instead of running a real OS process.
type e2eFakeProc struct {
	name   string
	startC chan string
	done   chan struct{}
}

func (f *e2eFakeProc) Start(_ context.Context, _ []string, _ domain.LogConfig) error {
	select {
	case f.startC <- f.name:
	default:
	}
	close(f.done)
	return nil
}
func (f *e2eFakeProc) Wait() (int, error)         { <-f.done; return 0, nil }
func (f *e2eFakeProc) Signal(os.Signal) error     { return nil }
func (f *e2eFakeProc) Kill() error                { return nil }
func (f *e2eFakeProc) PID() int                   { return 1 }
func (f *e2eFakeProc) Done() <-chan struct{}      { return f.done }
func (f *e2eFakeProc) Output() (string, string)   { return "", "" }

// TestScriptEndToEndManualTrigger runs the real flow: a JS script registers a
// user-defined route, then chain.run's a scheduled node (blocking). The test
// fires the route, which calls scheduler.trigger -> orchestrator -> the
// scheduled node's process starts. Verifies the goja runtime + orchestrator +
// api wiring end to end.
func TestScriptEndToEndManualTrigger(t *testing.T) {
	startC := make(chan string, 8)
	router := echo.New()
	log := system.NewLogger()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Shared factory: both the orchestrator and process.spawn return recording
	// fakes so we can observe the scheduled node's process start.
	factory := func(node domain.ProcessNode) process.ProcessRepository {
		return &e2eFakeProc{name: node.Name, startC: startC, done: make(chan struct{})}
	}
	orch := orchestrator.NewService(factory, log, nil)

	reg := runtime.NewRegistry()
	registerTestHostModule(reg, ctx, log, factory, orch, router)
	engine := NewEngine(reg)

	src := `
		const { chain, scheduler, api } = require("ezx");
		// Register the manual-trigger route before blocking in chain.run.
		api.post("/backup/full", () => {
			const fired = scheduler.trigger("backup-full");
			return { ok: fired, inflight: scheduler.status("backup-full").inflight };
		});
		// chain.run blocks supervising a scheduled node (yearly cron, never fires).
		chain.run({ roots: [{
			name: "backup-full",
			process: { binaryPath: "/bin/true" },
			scheduler: scheduler.build({
				schedule: { expression: "0 0 1 1 *" },
				minInterval: 1e0,
			}),
		}] });
	`

	runErr := make(chan error, 1)
	go func() { runErr <- engine.RunString(ctx, src) }()

	// Wait for the scheduled node's trigger to be registered.
	deadline := time.Now().Add(5 * time.Second)
	for !orch.Scheduled("backup-full") {
		if time.Now().After(deadline) {
			t.Fatal("scheduled node never registered")
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Fire the user route; it should trigger the scheduled tick.
	req := httptest.NewRequest(http.MethodPost, "/backup/full", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /backup/full code = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("body = %q, want ok:true", rec.Body.String())
	}

	select {
	case n := <-startC:
		if n != "backup-full" {
			t.Fatalf("started %q, want backup-full", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("scheduled node process never started after manual trigger")
	}

	cancel()
	select {
	case err := <-runErr:
		// On cancel the script is interrupted (context cancelled) as the chain
		// drains — the designed SIGTERM shutdown path.
		if err != nil && !strings.Contains(err.Error(), "context cancelled") {
			t.Fatalf("RunString returned unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunString did not return after cancel")
	}
}

func TestScriptProcessRunCapture(t *testing.T) {
	engine, _ := buildTestEngine(t)

	src := `
		const { process } = require("ezx");
		// run returns exit code.
		const c = process.run({ name: "sh", process: { binaryPath: "/bin/sh", arguments: ["-c", "exit 3"] } });
		if (c !== 3) throw new Error("run expected exit 3, got " + c);
		// run with check throws on non-zero.
		let threw = false;
		try { process.run({ process: { binaryPath: "/bin/sh", arguments: ["-c", "exit 1"] }, check: true }); }
		catch (e) { threw = true; }
		if (!threw) throw new Error("run check should throw on non-zero");
		// capture returns { code, stdout, stderr }.
		const cap = process.capture({ process: { binaryPath: "/bin/echo", arguments: ["hello"] } });
		if (cap.code !== 0) throw new Error("capture code = " + cap.code);
		if (cap.stdout !== "hello\n") throw new Error("capture stdout = " + JSON.stringify(cap.stdout));
		// capture with check throws on non-zero including stderr.
		threw = false;
		try { process.capture({ process: { binaryPath: "/bin/sh", arguments: ["-c", "echo oops >&2; exit 2"] }, check: true }); }
		catch (e) { threw = true; }
		if (!threw) throw new Error("capture check should throw on non-zero");
	`
	if err := engine.RunString(context.Background(), src); err != nil {
		t.Fatalf("RunString: %v", err)
	}
}

func TestScriptProcessShellAndShellQuote(t *testing.T) {
	engine, _ := buildTestEngine(t)

	src := `
		const { process, shell } = require("ezx");
		const c = process.shell("exit 0", {});
		if (c !== 0) throw new Error("shell exit = " + c);
		let threw = false;
		try { process.shell("exit 7", { check: true }); } catch (e) { threw = true; }
		if (!threw) throw new Error("shell check should throw on non-zero");
		const q = shell.quote("it's a value");
		if (q !== "'it'\\''s a value'") throw new Error("quote wrong: " + q);
	`
	if err := engine.RunString(context.Background(), src); err != nil {
		t.Fatalf("RunString: %v", err)
	}
}

func TestScriptFSWriteEnsureDirWhich(t *testing.T) {
	dir := t.TempDir()
	engine, _ := buildTestEngine(t)

	path := filepath.Join(dir, "sub", "f.txt")
	src := fmt.Sprintf(`
		const { fs } = require("ezx");
		fs.write(%q, "content");
		const info = fs.stat(%q);
		if (info.name !== "f.txt") throw new Error("stat name = " + info.name);
		fs.ensureDir(%q, { mode: 0o700 });
		if (!fs.exists(%q)) throw new Error("ensureDir did not create dir");
		if (!fs.which("sh")) throw new Error("which(sh) should be true");
		if (fs.which("ezx-no-such-binary")) throw new Error("which(bogus) should be false");
	`, path, path, filepath.Join(dir, "a", "b"), filepath.Join(dir, "a", "b"))
	if err := engine.RunString(context.Background(), src); err != nil {
		t.Fatalf("RunString: %v", err)
	}
}

func TestScriptEnvIntBoolList(t *testing.T) {
	t.Setenv("EZX_JS_INT", "42")
	t.Setenv("EZX_JS_BOOL", "true")
	t.Setenv("EZX_JS_LIST", "a, b,,c")
	engine, _ := buildTestEngine(t)

	src := `
		const { env } = require("ezx");
		if (env.int("EZX_JS_INT") !== 42) throw new Error("int failed");
		if (env.int("EZX_JS_INT_UNSET", 7) !== 7) throw new Error("int default failed");
		let threw = false;
		try { env.int("EZX_JS_BAD"); } catch (e) { threw = true; }
		if (!threw) throw new Error("int non-integer should throw");
		if (env.bool("EZX_JS_BOOL") !== true) throw new Error("bool failed: " + env.bool("EZX_JS_BOOL"));
		if (env.bool("EZX_JS_UNSET") !== false) throw new Error("bool unset should be false");
		if (env.bool("EZX_JS_UNSET", true) !== true) throw new Error("bool default true failed");
		const l = env.list("EZX_JS_LIST", ",");
		if (l.length !== 3 || l[0] !== "a" || l[1] !== "b" || l[2] !== "c") throw new Error("list failed: " + JSON.stringify(l));
	`
	if err := engine.RunString(context.Background(), src); err != nil {
		t.Fatalf("RunString: %v", err)
	}
}

// TestScriptSchedulerEvery verifies scheduler.every returns a valid
// SchedulerConfig and that a callback tick does not spawn a process.
func TestScriptSchedulerEvery(t *testing.T) {
	startC := make(chan string, 8)
	router := echo.New()
	log := system.NewLogger()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	factory := func(node domain.ProcessNode) process.ProcessRepository {
		return &e2eFakeProc{name: node.Name, startC: startC, done: make(chan struct{})}
	}
	orch := orchestrator.NewService(factory, log, nil)

	reg := runtime.NewRegistry()
	registerTestHostModule(reg, ctx, log, factory, orch, router)
	engine := NewEngine(reg)

	// every() returns a config with a JS callback tick; the callback sets a
	// global side effect. chain.run supervises the scheduled node.
	src := `
		const { chain, scheduler } = require("ezx");
		const sched = scheduler.every("*/1 * * * *", () => { globalThis.__ticked = true; }, { minInterval: 1e0 });
		chain.run({ roots: [{
			name: "every-job",
			process: { binaryPath: "/bin/true" },
			scheduler: sched,
		}] });
	`

	runErr := make(chan error, 1)
	go func() { runErr <- engine.RunString(ctx, src) }()

	deadline := time.Now().Add(5 * time.Second)
	for !orch.Scheduled("every-job") {
		if time.Now().After(deadline) {
			t.Fatal("scheduled node never registered")
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Fire a manual trigger; the callback (not a process spawn) should run.
	if !orch.Trigger("every-job") {
		t.Fatal("Trigger(every-job) returned false")
	}

	// A callback tick must NOT spawn a process. Give the tick a moment.
	time.Sleep(50 * time.Millisecond)
	select {
	case n := <-startC:
		t.Fatalf("callback tick should not spawn a process, but %q started", n)
	default:
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil && !strings.Contains(err.Error(), "context cancelled") {
			t.Fatalf("RunString returned unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunString did not return after cancel")
	}
}

// TestScriptLifecycleCallbacks verifies onStart/onExit fire through the
// orchestrator. Each callback writes a marker file so the Go test can observe
// the side effect deterministically.
func TestScriptLifecycleCallbacks(t *testing.T) {
	dir := t.TempDir()
	startMarker := filepath.Join(dir, "onstart")
	exitMarker := filepath.Join(dir, "onexit")
	router := echo.New()
	log := system.NewLogger()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	factory := func(node domain.ProcessNode) process.ProcessRepository {
		return &e2eFakeProc{name: node.Name, startC: make(chan string, 8), done: make(chan struct{})}
	}
	orch := orchestrator.NewService(factory, log, nil)

	reg := runtime.NewRegistry()
	registerTestHostModule(reg, ctx, log, factory, orch, router)
	engine := NewEngine(reg)

	// The fake proc exits immediately, so onStart fires after start and onExit
	// after the process exits. Callbacks write marker files via fs.write. A
	// restart policy opts the lone leaf out of the exec default so it stays
	// supervised.
	src := fmt.Sprintf(`
		const { chain, fs } = require("ezx");
		chain.run({ roots: [{
			name: "cb-node",
			process: { binaryPath: "/bin/true" },
			restart: { mode: "never" },
			onStart: () => { fs.write(%q, "1"); },
			onExit: (code) => { fs.write(%q, String(code)); },
		}] });
	`, startMarker, exitMarker)

	if err := engine.RunString(context.Background(), src); err != nil {
		t.Fatalf("RunString: %v", err)
	}

	if !scriptTestFileExists(startMarker) {
		t.Fatal("onStart callback did not fire (marker missing)")
	}
	if !scriptTestFileExists(exitMarker) {
		t.Fatal("onExit callback did not fire (marker missing)")
	}
}

func scriptTestFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestScriptReadinessFunc verifies that a JS `readinessFunc: () => ...` property
// on a node binds to the Go ReadinessFunc callback and gates a needParentReady
// child: the child starts only after the callback returns true.
func TestScriptReadinessFunc(t *testing.T) {
	startC := make(chan string, 8)
	router := echo.New()
	log := system.NewLogger()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	factory := func(node domain.ProcessNode) process.ProcessRepository {
		return &e2eFakeProc{name: node.Name, startC: startC, done: make(chan struct{})}
	}
	orch := orchestrator.NewService(factory, log, nil)

	reg := runtime.NewRegistry()
	registerTestHostModule(reg, ctx, log, factory, orch, router)
	engine := NewEngine(reg)

	// readiness returns false once, then true. The needParentReady child must
	// only start after the callback returns true (i.e. after the parent).
	src := `
		const { chain } = require("ezx");
		let calls = 0;
		chain.run({ roots: [{
			name: "pg",
			process: { binaryPath: "/bin/true" },
			restart: { mode: "never" },
			readinessFunc: () => { calls++; return calls >= 2; },
			children: [{ name: "sidecar", needParentReady: true, process: { binaryPath: "/bin/true" } }],
		}] });
	`

	if err := engine.RunString(context.Background(), src); err != nil {
		t.Fatalf("RunString: %v", err)
	}

	// Both processes must start, parent before child (child gated on readiness).
	var order []string
	for i := 0; i < 2; i++ {
		select {
		case n := <-startC:
			order = append(order, n)
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for starts")
		}
	}
	if len(order) != 2 || order[0] != "pg" || order[1] != "sidecar" {
		t.Fatalf("start order = %v, want [pg sidecar]", order)
	}
}

// TestRunFileRelativeRequire verifies that RunFile resolves relative
// require("./x.js") from the entry script's directory, enabling multi-file
// bootstrap scripts.
func TestRunFileRelativeRequire(t *testing.T) {
	dir := t.TempDir()

	helper := filepath.Join(dir, "helper.js")
	if err := os.WriteFile(helper, []byte("module.exports = { greet: function() { return \"hi\"; } };\n"), 0o644); err != nil {
		t.Fatalf("write helper: %v", err)
	}

	main := filepath.Join(dir, "main.js")
	mainSrc := `
		const helper = require("./helper.js");
		const msg = helper.greet();
		if (msg !== "hi") throw new Error("helper loaded wrong: " + msg);
		globalThis.__ok = true;
	`
	if err := os.WriteFile(main, []byte(mainSrc), 0o644); err != nil {
		t.Fatalf("write main: %v", err)
	}

	engine, _ := buildTestEngine(t)
	if err := engine.RunFile(context.Background(), main); err != nil {
		t.Fatalf("RunFile: %v", err)
	}
}
