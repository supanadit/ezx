package system

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/process"
	"github.com/supanadit/ezx/script"
	scriptmodules "github.com/supanadit/ezx/script/modules"
)

// buildTestEngine wires a ScriptEngine with the env + editor host modules and a
// capturing logger, for exercising host APIs from JS.
func buildTestEngine(t *testing.T) (*ScriptEngine, *bytes.Buffer) {
	t.Helper()
	reg := script.NewRegistry()
	var buf bytes.Buffer
	log := NewLogger()
	log.SetOutput(&buf)
	reg.Register("ezx", func() any {
		return scriptmodules.NewEzxModule(context.Background(), log, func(node domain.ProcessNode) process.ProcessRepository {
			return NewProcessRepository(node, nil)
		}, nil, nil)
	})
	return NewScriptEngine(reg), &buf
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
