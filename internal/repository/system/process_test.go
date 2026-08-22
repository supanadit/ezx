package system

import (
	"context"
	"os"
	"reflect"
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
