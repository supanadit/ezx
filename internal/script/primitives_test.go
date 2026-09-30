package script

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/internal/repository/system"
	"github.com/supanadit/ezx/process"
)

// realProcFactory returns a local-OS process repository for tests that need
// to actually run a command.
func realProcFactory() ProcessFactory {
	return func(node domain.ProcessNode) process.ProcessRepository {
		return system.NewProcessRepository(node, nil)
	}
}

func TestProcessRunReturnsExitCode(t *testing.T) {
	m := NewProcessModule(context.Background(), realProcFactory(), system.NewAdapter(), nil, nil)

	code, err := m.Run(runOpts{Process: domain.Process{BinaryPath: "/bin/sh", Arguments: []string{"-c", "exit 3"}}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 3 {
		t.Fatalf("code = %d, want 3", code)
	}
}

func TestProcessRunCheckThrowsOnNonZero(t *testing.T) {
	m := NewProcessModule(context.Background(), realProcFactory(), system.NewAdapter(), nil, nil)

	_, err := m.Run(runOpts{Process: domain.Process{BinaryPath: "/bin/sh", Arguments: []string{"-c", "exit 4"}}, Check: true})
	if err == nil {
		t.Fatal("expected error on non-zero exit with check=true")
	}
	if !strings.Contains(err.Error(), "exit") {
		t.Fatalf("error = %v, want it to mention exit", err)
	}
}

// TestProcessRunCheckOkOnZero verifies check=true on a clean exit returns 0.
func TestProcessRunCheckOkOnZero(t *testing.T) {
	m := NewProcessModule(context.Background(), realProcFactory(), system.NewAdapter(), nil, nil)

	code, err := m.Run(runOpts{Process: domain.Process{BinaryPath: "/bin/true"}, Check: true})
	if err != nil {
		t.Fatalf("Run check=true on exit 0: %v", err)
	}
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
}

// TestProcessRunTimeoutKillsAndReports pins the timeout contract: an
// opts.Timeout that elapses must kill the process and surface a "timed out"
// error. It used to pass for the wrong reason — the process adapter spawns via
// exec.CommandContext, whose cancellation watcher SIGKILLed the child the
// instant the derived context expired, so the call returned an exit-code error
// before the explicit timeout check could ever be reached.
func TestProcessRunTimeoutKillsAndReports(t *testing.T) {
	m := NewProcessModule(context.Background(), realProcFactory(), system.NewAdapter(), nil, nil)

	start := time.Now()
	// `exec sleep 30` replaces the shell, so the kill lands on the sleeping
	// process (a signal to a shell blocked in `sleep 30` would be handled only
	// after sleep returns).
	code, err := m.Run(runOpts{
		Process: domain.Process{BinaryPath: "/bin/sh", Arguments: []string{"-c", "exec sleep 30"}},
		Timeout: int64(300 * time.Millisecond),
	})
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v, want it to report the timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("timeout took %v, want it bounded by the 300ms budget", elapsed)
	}
	if code == 0 {
		t.Fatalf("killed process reported exit code 0")
	}
}

// TestProcessHandleCancelSignalsNotKills pins the spawn cancellation contract:
// cancelling the module context must interrupt a spawned process with a signal
// it can trap (graceful), never an uncatchable SIGKILL. Before the fix this was
// a SIGKILL from os/exec's cancellation watcher, so a trap-based shutdown never
// ran.
func TestProcessHandleCancelSignalsNotKills(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "term")
	ctx, cancel := context.WithCancel(context.Background())
	m := NewProcessModule(ctx, realProcFactory(), system.NewAdapter(), nil, nil)

	h := m.Spawn(domain.ProcessNode{
		Name: "trapper",
		Process: domain.Process{
			BinaryPath: "/bin/sh",
			Arguments:  []string{"-c", `trap 'echo trapped > "$MARKER"; exit 0' TERM; while true; do sleep 0.1; done`},
			Environment: []string{
				"MARKER=" + marker,
			},
		},
	})
	if err := h.Start(nil); err != nil {
		t.Fatalf("Start: %v", err)
	}

	time.Sleep(500 * time.Millisecond)
	cancel()

	waitErr := make(chan error, 1)
	go func() {
		_, err := h.Wait()
		waitErr <- err
	}()
	select {
	case err := <-waitErr:
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("process did not exit after cancellation (was it SIGKILLed?)")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("cancellation did not reach the process as a catchable signal: %v", err)
	}
}

func TestProcessCaptureStdout(t *testing.T) {
	m := NewProcessModule(context.Background(), realProcFactory(), system.NewAdapter(), nil, nil)

	res, err := m.Capture(runOpts{Process: domain.Process{BinaryPath: "/bin/echo", Arguments: []string{"hello"}}})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if res["code"] != 0 {
		t.Fatalf("code = %v, want 0", res["code"])
	}
	if res["stdout"] != "hello\n" {
		t.Fatalf("stdout = %q, want %q", res["stdout"], "hello\n")
	}
}

func TestProcessCaptureCheckThrowsWithStderr(t *testing.T) {
	m := NewProcessModule(context.Background(), realProcFactory(), system.NewAdapter(), nil, nil)

	_, err := m.Capture(runOpts{
		Process: domain.Process{BinaryPath: "/bin/sh", Arguments: []string{"-c", "echo oops >&2; exit 2"}},
		Check:   true,
	})
	if err == nil {
		t.Fatal("expected error on non-zero capture, got nil")
	}
	if !strings.Contains(err.Error(), "oops") {
		t.Fatalf("err = %v, want it to include stderr", err)
	}
}

func TestProcessShell(t *testing.T) {
	m := NewProcessModule(context.Background(), realProcFactory(), system.NewAdapter(), nil, nil)

	code, err := m.Shell("exit 0", shellOpts{})
	if err != nil {
		t.Fatalf("Shell: %v", err)
	}
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}

	_, err = m.Shell("exit 5", shellOpts{Check: true})
	if err == nil {
		t.Fatal("Shell with check on non-zero should error")
	}
}

func TestFSWriteCreatesFileWithMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "file.txt")
	m := NewFSModule()

	got, err := m.Write(path, "content", writeOpts{Mode: 0o600})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "content" {
		t.Fatalf("content = %q, want %q", string(data), "content")
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestFSEnsureDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "c")
	m := NewFSModule()

	if err := m.EnsureDir(path, writeOpts{Mode: 0o700}); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("EnsureDir did not create a directory")
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %v, want 0700", info.Mode().Perm())
	}
}

func TestFSWhich(t *testing.T) {
	m := NewFSModule()
	if !m.Which("sh") {
		t.Fatal("Which(sh) = false, want true")
	}
	if m.Which("ezx-definitely-not-a-real-binary-xyz") {
		t.Fatal("Which(bogus) = true, want false")
	}
}

func TestEnvInt(t *testing.T) {
	m := NewEnvModule()
	t.Setenv("EZX_INT", "42")
	n, err := m.Int("EZX_INT")
	if err != nil || n != 42 {
		t.Fatalf("Int = %d, %v; want 42", n, err)
	}
	// default when unset
	if n, err := m.Int("EZX_INT_UNSET", 7); err != nil || n != 7 {
		t.Fatalf("Int(unset, 7) = %d, %v; want 7", n, err)
	}
	// non-integer throws
	t.Setenv("EZX_BAD_INT", "abc")
	if _, err := m.Int("EZX_BAD_INT"); err == nil {
		t.Fatal("Int(non-integer) should error")
	}
}

func TestEnvBool(t *testing.T) {
	m := NewEnvModule()
	t.Setenv("EZX_BOOL", "true")
	if !m.Bool("EZX_BOOL") {
		t.Fatal("Bool(true) = false, want true")
	}
	t.Setenv("EZX_BOOL", "off")
	if m.Bool("EZX_BOOL") {
		t.Fatal("Bool(off) = true, want false")
	}
	if m.Bool("EZX_BOOL_UNSET") {
		t.Fatal("Bool(unset) = true, want false")
	}
	if !m.Bool("EZX_BOOL_UNSET", true) {
		t.Fatal("Bool(unset, true) = false, want true")
	}
}

func TestEnvList(t *testing.T) {
	m := NewEnvModule()
	t.Setenv("EZX_LIST", "a, b,,c")
	got := m.List("EZX_LIST", ",")
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List = %v, want %v", got, want)
	}
	// unset returns default (or empty)
	if got := m.List("EZX_LIST_UNSET", ","); len(got) != 0 {
		t.Fatalf("List(unset) = %v, want empty", got)
	}
	if got := m.List("EZX_LIST_UNSET", ",", []string{"x"}); !reflect.DeepEqual(got, []string{"x"}) {
		t.Fatalf("List(unset, def) = %v, want [x]", got)
	}
}

func TestShellQuote(t *testing.T) {
	m := NewShellModule()
	if got := m.Quote("it's a value"); got != "'it'\\''s a value'" {
		t.Fatalf("Quote = %q, want %q", got, "'it'\\''s a value'")
	}
	if got := m.Quote("plain"); got != "'plain'" {
		t.Fatalf("Quote(plain) = %q, want 'plain'", got)
	}
}

func TestProcessCaptureStreamingCallback(t *testing.T) {
	var lines []string
	m := NewProcessModule(context.Background(), realProcFactory(), system.NewAdapter(), &collectInvoker{fn: func(arg any) { lines = append(lines, arg.(string)) }}, nil)

	_, err := m.Capture(runOpts{
		Process:  domain.Process{BinaryPath: "/bin/echo", Arguments: []string{"a\nb"}},
		OnStdout: "callback",
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if !reflect.DeepEqual(lines, []string{"a", "b"}) {
		t.Fatalf("callback lines = %v, want [a b]", lines)
	}
}

// collectInvoker is a minimal runtime.Invoker that records each arg string.
type collectInvoker struct {
	fn func(any)
}

func (c *collectInvoker) Call(fn any, args ...any) (any, error) {
	for _, a := range args {
		c.fn(a)
	}
	return nil, nil
}
