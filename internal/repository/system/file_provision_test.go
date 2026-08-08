package system

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/supanadit/ezx/domain"
)

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestProvisionFile_ReplaceFromEnv(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "secret.txt")
	t.Setenv("MY_SECRET", "s3cr3t")

	fp := domain.FileProvision{
		Path: target,
		Operations: []domain.FileOperation{
			{Type: domain.FileOpReplace, FromEnv: "MY_SECRET"},
		},
	}
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	if got := read(t, target); got != "s3cr3t" {
		t.Fatalf("content = %q, want %q", got, "s3cr3t")
	}
}

func TestProvisionFile_ConditionGatesProvision(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "conf")
	t.Setenv("NODE_ROLE", "standby")

	fp := domain.FileProvision{
		Path: target,
		When: domain.EnvCondition{Name: "NODE_ROLE", Value: "primary"},
		Operations: []domain.FileOperation{
			{Type: domain.FileOpReplace, Value: "should-not-write"},
		},
	}
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("file should not be created when condition fails")
	}
}

func TestProvisionFile_SetPropertyIdempotent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "pgbackrest.conf")
	write(t, target, "pg1-host=x\npg2-host=y\nbackup-standby=y\n")
	t.Setenv("NODE_ROLE", "primary")

	fp := domain.FileProvision{
		Path: target,
		When: domain.EnvCondition{Name: "NODE_ROLE", Value: "primary"},
		Operations: []domain.FileOperation{
			{Type: domain.FileOpRemove, Pattern: "^pg2-"},
			{Type: domain.FileOpSetProperty, Pattern: "^backup-standby=", Value: "backup-standby=n"},
		},
	}
	for i := 0; i < 2; i++ {
		if err := provisionFile(fp); err != nil {
			t.Fatalf("provisionFile pass %d: %v", i+1, err)
		}
	}
	got := read(t, target)
	want := "pg1-host=x\nbackup-standby=n\n"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestProvisionFile_FromEnvPatternWithNameTransform(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "postgresql.conf")
	t.Setenv("POSTGRESQL_CONFIG_SHARED_BUFFERS", "128MB")
	t.Setenv("POSTGRESQL_CONFIG_MAX_CONNECTIONS", "100")
	t.Setenv("UNRELATED", "x")

	fp := domain.FileProvision{
		Path: target,
		Operations: []domain.FileOperation{
			{
				Type:           domain.FileOpSetProperty,
				FromEnvPattern: "^POSTGRESQL_CONFIG_(.+)$",
				NameTransform:  domain.NameTransformLower,
				Pattern:        "^[[:space:]]*#?[[:space:]]*${name}[[:space:]]*=.*",
				Value:          "${name} = ${value}",
				ValueFormat:    domain.ValueFormatAuto,
			},
		},
	}
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	got := read(t, target)
	want := "shared_buffers = 128MB\nmax_connections = 100\n"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestProvisionFile_SetBlockInjection(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "wp-config.php")
	write(t, target, "<?php\ndefine('DB_HOST', 'db');\nrequire_once ABSPATH . 'wp-settings.php';\n")
	t.Setenv("WORDPRESS_WP_REDIS_CONFIG", "['token' => 'abc',\n'host' => 'redis',]")

	fp := domain.FileProvision{
		Path: target,
		Operations: []domain.FileOperation{
			{
				Type:     domain.FileOpSetBlock,
				FromEnv:  "WORDPRESS_WP_REDIS_CONFIG",
				Pattern:  "define\\(.*WP_REDIS_CONFIG",
				BlockEnd: "\\);",
				Marker:   "require_once.*wp-settings",
				Value:    "define('WP_REDIS_CONFIG', ${value});",
			},
		},
	}
	// First run: block absent, inserted before marker.
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	got1 := read(t, target)
	want1 := "<?php\ndefine('DB_HOST', 'db');\ndefine('WP_REDIS_CONFIG', ['token' => 'abc',\n'host' => 'redis',]);\nrequire_once ABSPATH . 'wp-settings.php';\n"
	if got1 != want1 {
		t.Fatalf("first run content = %q, want %q", got1, want1)
	}
	// Second run: block exists, replaced (no duplicate).
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile second: %v", err)
	}
	got2 := read(t, target)
	if got2 != want1 {
		t.Fatalf("second run content = %q, want %q (idempotent)", got2, want1)
	}
}

func TestProvisionFile_InsertBeforeNonIdempotent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "wp-config.php")
	write(t, target, "<?php\nrequire_once ABSPATH . 'wp-settings.php';\n")
	t.Setenv("IS_PROTECT_WPCONFIG", "true")

	fp := domain.FileProvision{
		Path: target,
		Operations: []domain.FileOperation{
			{
				Type:    domain.FileOpInsertBefore,
				Pattern: "require_once.*wp-settings",
				Value:   "if (!defined('ABSPATH')) exit;\n",
				When:    domain.EnvCondition{Name: "IS_PROTECT_WPCONFIG", Value: "true"},
			},
		},
	}
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	got := read(t, target)
	want := "<?php\nif (!defined('ABSPATH')) exit;\nrequire_once ABSPATH . 'wp-settings.php';\n"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestProvisionFile_CopyFromEnvPattern(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "dropins")
	wpDir := filepath.Join(dir, "wp-content")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(srcDir, "object-cache.php"), "<?php // drop-in")
	t.Setenv("STATELESS_FILE_OBJECT_CACHE", "object-cache.php")

	fp := domain.FileProvision{
		Path: filepath.Join(wpDir, "${value}"),
		Operations: []domain.FileOperation{
			{
				Type:           domain.FileOpCopy,
				FromEnvPattern: "^STATELESS_FILE_(.+)$",
				Source:         filepath.Join(srcDir, "${value}"),
			},
		},
	}
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	if got := read(t, filepath.Join(wpDir, "object-cache.php")); got != "<?php // drop-in" {
		t.Fatalf("copied content = %q", got)
	}
}

func TestProvisionFile_PermissionApplied(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "secret.conf")
	fp := domain.FileProvision{
		Path:       target,
		Permission: 0o640,
		Operations: []domain.FileOperation{
			{Type: domain.FileOpReplace, Value: "x"},
		},
	}
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("permission = %v, want 0640", got)
	}
}
