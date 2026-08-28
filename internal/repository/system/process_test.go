package system

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/internal/repository"
)

func TestBuildProcessEnvNoFilter(t *testing.T) {
	parent := []string{"PATH=/usr/bin", "HOME=/root"}
	env, err := repository.BuildProcessEnv(parent, domain.Process{})
	if err != nil {
		t.Fatalf("repository.BuildProcessEnv: %v", err)
	}
	if !reflect.DeepEqual(env, parent) {
		t.Fatalf("env = %v, want parent unchanged %v", env, parent)
	}
}

func TestBuildProcessEnvExactFilter(t *testing.T) {
	parent := []string{"ETCD_NAME=n1", "ETCD_DATA_DIR=/data", "PATH=/usr/bin"}
	env, err := repository.BuildProcessEnv(parent, domain.Process{
		FilterEnv: []string{"ETCD_NAME", "ETCD_DATA_DIR"},
	})
	if err != nil {
		t.Fatalf("repository.BuildProcessEnv: %v", err)
	}
	want := []string{"PATH=/usr/bin"}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("env = %v, want %v", env, want)
	}
}

func TestBuildProcessEnvPatternFilter(t *testing.T) {
	parent := []string{
		"PGBACKREST_REPO1_TYPE=s3",
		"PGBACKREST_STANZA=main",
		"POSTGRESQL_DB=app",
	}
	env, err := repository.BuildProcessEnv(parent, domain.Process{
		FilterEnvPattern: []string{"^PGBACKREST_"},
	})
	if err != nil {
		t.Fatalf("repository.BuildProcessEnv: %v", err)
	}
	want := []string{"POSTGRESQL_DB=app"}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("env = %v, want %v", env, want)
	}
}

func TestBuildProcessEnvMinIOSelective(t *testing.T) {
	parent := []string{
		"MINIO_DATA_DIR=/data",
		"MINIO_ADDRESS=:9000",
		"MINIO_ROOT_USER=admin",
		"MINIO_ROOT_PASSWORD=secret",
	}
	env, err := repository.BuildProcessEnv(parent, domain.Process{
		FilterEnv: []string{"MINIO_DATA_DIR", "MINIO_ADDRESS"},
	})
	if err != nil {
		t.Fatalf("repository.BuildProcessEnv: %v", err)
	}
	want := []string{"MINIO_ROOT_USER=admin", "MINIO_ROOT_PASSWORD=secret"}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("env = %v, want %v", env, want)
	}
}

func TestBuildProcessEnvAdditionsOverrideFilter(t *testing.T) {
	parent := []string{"PGBACKREST_STANZA=main"}
	env, err := repository.BuildProcessEnv(parent, domain.Process{
		FilterEnvPattern: []string{"^PGBACKREST_"},
		Environment:      []string{"PGBACKREST_STANZA=forced"},
	})
	if err != nil {
		t.Fatalf("repository.BuildProcessEnv: %v", err)
	}
	want := []string{"PGBACKREST_STANZA=forced"}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("env = %v, want %v", env, want)
	}
}

func TestBuildProcessEnvInvalidPattern(t *testing.T) {
	if _, err := repository.BuildProcessEnv([]string{"A=1"}, domain.Process{
		FilterEnvPattern: []string{"("},
	}); err == nil {
		t.Fatal("repository.BuildProcessEnv with invalid pattern should return error")
	}
}

// startSh check helper: run /bin/sh -c "$check" with the given env slice and
// return the exit code.
func startSh(t *testing.T, env []string, check string) int {
	t.Helper()
	repo := NewProcessRepository(domain.ProcessNode{
		Name: "sh",
		Process: domain.Process{
			BinaryPath: "/bin/sh",
			Arguments:  []string{"-c", check},
		},
	}, nil)
	if err := repo.Start(context.Background(), env, domain.LogConfig{
		Stdout: domain.LogDestDiscard,
		Stderr: domain.LogDestDiscard,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	code, err := repo.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	return code
}

// TestStartEmptyEnvInheritsParent verifies that spawning with an empty env slice
// inherits the parent environment (so PATH is present) instead of launching with
// a truly empty environment.
func TestStartEmptyEnvInheritsParent(t *testing.T) {
	t.Setenv("EZX_INHERIT_TEST", "yes")
	code := startSh(t, []string{}, `[ "$EZX_INHERIT_TEST" = "yes" ]`)
	if code != 0 {
		t.Fatalf("empty env did not inherit parent env: exit=%d", code)
	}
}

// TestStartNilEnvInheritsParent verifies nil env also inherits.
func TestStartNilEnvInheritsParent(t *testing.T) {
	t.Setenv("EZX_INHERIT_TEST", "yes")
	code := startSh(t, nil, `[ "$EZX_INHERIT_TEST" = "yes" ]`)
	if code != 0 {
		t.Fatalf("nil env did not inherit parent env: exit=%d", code)
	}
}

// TestStartExplicitEnvNotInherited verifies that a non-empty explicit env is
// used as-is (the marker variable is absent → check fails).
func TestStartExplicitEnvNotInherited(t *testing.T) {
	t.Setenv("EZX_INHERIT_TEST", "yes")
	code := startSh(t, []string{"OTHER=1"}, `[ "$EZX_INHERIT_TEST" = "yes" ]`)
	if code != 1 {
		t.Fatalf("explicit env unexpectedly inherited parent: exit=%d, want 1", code)
	}
}

// TestStartEnvIncludesParentPath verifies PATH from the parent is present when
// inheriting, so a binary on PATH resolves.
func TestStartEnvIncludesParentPath(t *testing.T) {
	if os.Getenv("PATH") == "" {
		t.Skip("PATH not set in test environment")
	}
	code := startSh(t, []string{}, `command -v sh >/dev/null 2>&1`)
	if code != 0 {
		t.Fatalf("PATH not inherited: exit=%d", code)
	}
}

// newReaperProc spawns a process through the shared reaper and returns the
// wait exit code.
func newReaperProc(t *testing.T, shCmd string) int {
	t.Helper()
	reaper := NewReaper(nil)
	if err := reaper.Start(context.Background()); err != nil {
		t.Fatalf("reaper.Start: %v", err)
	}
	defer reaper.Stop()
	repo := NewProcessRepository(domain.ProcessNode{
		Name: "sh",
		Process: domain.Process{
			BinaryPath: "/bin/sh",
			Arguments:  []string{"-c", shCmd},
		},
	}, reaper)
	if err := repo.Start(context.Background(), []string{}, domain.LogConfig{
		Stdout: domain.LogDestDiscard,
		Stderr: domain.LogDestDiscard,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	code, err := repo.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	return code
}

// TestStartReaperExitCodeZero verifies Wait returns 0 for a clean exit through
// the reaper (ProcessState is unpopulated in that path).
func TestStartReaperExitCodeZero(t *testing.T) {
	if code := newReaperProc(t, "exit 0"); code != 0 {
		t.Fatalf("reaper exit code = %d, want 0", code)
	}
}

// TestStartReaperExitCodeNonZero verifies Wait returns the real non-zero exit
// code through the reaper.
func TestStartReaperExitCodeNonZero(t *testing.T) {
	if code := newReaperProc(t, "exit 3"); code != 3 {
		t.Fatalf("reaper exit code = %d, want 3", code)
	}
}

// startFileProc spawns /bin/sh -c "$cmd" with both streams routed to a rotating
// file at path (maxBytes/maxBackups configurable) and returns the repository.
func startFileProc(t *testing.T, path string, maxBytes int64, maxBackups int, cmd string) *ProcessRepository {
	t.Helper()
	repo := NewProcessRepository(domain.ProcessNode{
		Name: "sh",
		Process: domain.Process{
			BinaryPath: "/bin/sh",
			Arguments:  []string{"-c", cmd},
		},
	}, nil)
	if err := repo.Start(context.Background(), []string{}, domain.LogConfig{
		Stdout:     domain.LogDestFile,
		Stderr:     domain.LogDestFile,
		FilePath:   path,
		MaxBytes:   maxBytes,
		MaxBackups: maxBackups,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return repo
}

// TestStartFileLogStdout verifies a node with LogDestFile writes stdout to the
// file.
func TestStartFileLogStdout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.log")
	repo := startFileProc(t, path, 1<<20, 3, `echo "hello stdout"`)
	if _, err := repo.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(got), "hello stdout") {
		t.Fatalf("file = %q, want it to contain %q", got, "hello stdout")
	}
}

// TestStartFileLogStderr verifies a node with LogDestFile writes stderr to the
// file.
func TestStartFileLogStderr(t *testing.T) {
	path := filepath.Join(t.TempDir(), "err.log")
	repo := startFileProc(t, path, 1<<20, 3, `echo "hello stderr" >&2`)
	if _, err := repo.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(got), "hello stderr") {
		t.Fatalf("file = %q, want it to contain %q", got, "hello stderr")
	}
}

// TestStartFileLogBothStreamsSharedPath verifies stdout+stderr routing to the
// same path share one writer and produce one interleaved file.
func TestStartFileLogBothStreamsSharedPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "both.log")
	repo := startFileProc(t, path, 1<<20, 3, `echo "to-out"; echo "to-err" >&2`)
	if _, err := repo.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(got), "to-out") || !strings.Contains(string(got), "to-err") {
		t.Fatalf("file = %q, want it to contain both %q and %q", got, "to-out", "to-err")
	}
}

// TestStartFileLogRotates verifies a file-backed process rotates on disk when
// output exceeds maxBytes (tiny value).
func TestStartFileLogRotates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rot.log")
	// Emit 5 lines of 20 bytes each = 100 bytes; maxBytes=50 forces rotation.
	repo := startFileProc(t, path, 50, 3, `for i in 1 2 3 4 5; do echo "01234567890123456789"; done`)
	if _, err := repo.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	// At least one backup must exist.
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected a rotated backup %q, stat err = %v", path+".1", err)
	}
}

// TestStartFileLogWritersClosedOnExit verifies file writers are closed after the
// process exits (no fd leak): the repository's writer table is drained.
func TestStartFileLogWritersClosedOnExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "closed.log")
	repo := startFileProc(t, path, 1<<20, 3, `echo "hi"`)
	if _, err := repo.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	repo.fileMu.Lock()
	n := len(repo.fileWriters)
	repo.fileMu.Unlock()
	if n != 0 {
		t.Fatalf("fileWriters not drained after exit: %d writers remain", n)
	}
}

// TestStartFileLogRequiresFilePath verifies a file destination without a path is
// rejected defensively at spawn time.
func TestStartFileLogRequiresFilePath(t *testing.T) {
	repo := NewProcessRepository(domain.ProcessNode{
		Name: "sh",
		Process: domain.Process{
			BinaryPath: "/bin/sh",
			Arguments:  []string{"-c", "true"},
		},
	}, nil)
	err := repo.Start(context.Background(), []string{}, domain.LogConfig{
		Stdout: domain.LogDestFile,
	})
	if err == nil {
		t.Fatal("Start with file dest and empty filePath should error, got nil")
	}
	if !strings.Contains(err.Error(), "requires filePath") {
		t.Fatalf("Start error = %q, want it to mention requires filePath", err)
	}
}
