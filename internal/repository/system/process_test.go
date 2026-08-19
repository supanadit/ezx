package system

import (
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
