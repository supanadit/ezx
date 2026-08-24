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
