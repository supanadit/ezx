package system

import (
	"os"
	"path/filepath"
	"strings"
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

func TestNameTransformSnakeToDot(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "server.properties")
	t.Setenv("KAFKA_CONFIG_LOG_RETENTION_MS", "60000")
	t.Setenv("KAFKA_CONFIG_NUM_PARTITIONS", "3")

	fp := domain.FileProvision{
		Path: target,
		Operations: []domain.FileOperation{
			{
				Type:           domain.FileOpSetProperty,
				FromEnvPattern: "^KAFKA_CONFIG_(.+)$",
				NameTransform:  domain.NameTransformSnakeToDot,
				Pattern:        "^${name}=.*",
				Value:          "${name}=${value}",
			},
		},
	}
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	got := read(t, target)
	want := "log.retention.ms=60000\nnum.partitions=3\n"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestNameTransformSnakeToCamel(t *testing.T) {
	if got := snakeToCamel("SHARED_BUFFERS"); got != "sharedBuffers" {
		t.Fatalf("snakeToCamel(SHARED_BUFFERS) = %q, want %q", got, "sharedBuffers")
	}
	if got := snakeToCamel("already_camel"); got != "alreadyCamel" {
		t.Fatalf("snakeToCamel(already_camel) = %q, want %q", got, "alreadyCamel")
	}
}

func TestNameTransformSnakeToKebab(t *testing.T) {
	if got := applyNameTransform("SHARED_BUFFERS", domain.NameTransformSnakeToKebab); got != "shared-buffers" {
		t.Fatalf("got %q, want %q", got, "shared-buffers")
	}
}

func TestNameTransformFuncOverrides(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config")
	t.Setenv("CONFIG_MAX_LIMIT", "10")

	fp := domain.FileProvision{
		Path: target,
		Operations: []domain.FileOperation{
			{
				Type:           domain.FileOpSetProperty,
				FromEnvPattern: "^CONFIG_(.+)$",
				NameTransformFunc: func(name string) string {
					return "custom_" + strings.ToLower(name)
				},
				Pattern: "^${name}=.*",
				Value:   "${name}=${value}",
			},
		},
	}
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	got := read(t, target)
	want := "custom_max_limit=10\n"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestValueTransformFunc(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config")
	t.Setenv("SETTING_VALUE", "OFF")

	fp := domain.FileProvision{
		Path: target,
		Operations: []domain.FileOperation{
			{
				Type:    domain.FileOpSetProperty,
				FromEnv: "SETTING_VALUE",
				Pattern: "^setting=.*",
				Value:   "setting=${value}",
				ValueTransformFunc: func(value string) string {
					return strings.ToLower(value)
				},
			},
		},
	}
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	got := read(t, target)
	want := "setting=off\n"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestLineFuncPHPDefine(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "wp-config.php")
	write(t, target, "<?php\nrequire_once ABSPATH . 'wp-settings.php';\n")
	t.Setenv("WORDPRESS_DB_HOST", "mariadb:3306")
	t.Setenv("WORDPRESS_DEBUG", "true")

	fp := domain.FileProvision{
		Path: target,
		Operations: []domain.FileOperation{
			{
				Type:           domain.FileOpInsertBefore,
				FromEnvPattern: "^WORDPRESS_(.+)$",
				Pattern:        "require_once.*wp-settings",
				LineFunc: func(name, value string) string {
					if value == "true" || value == "false" {
						return "define('" + name + "', " + value + ");"
					}
					return "define('" + name + "', '" + value + "');"
				},
			},
		},
	}
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	got := read(t, target)
	want := "<?php\ndefine('DB_HOST', 'mariadb:3306');\ndefine('DEBUG', true);\nrequire_once ABSPATH . 'wp-settings.php';\n"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestContentFuncGeneratesFullContent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "z-mariadb.cnf")
	t.Setenv("MARIADB_MAX_CONNECTIONS", "300")
	t.Setenv("MARIADB_DATA_DIR", "/data")

	fp := domain.FileProvision{
		Path: target,
		ContentFunc: func(editor domain.FileEditor, environ []string) (string, error) {
			get := func(key, def string) string {
				for _, kv := range environ {
					if k, v, ok := strings.Cut(kv, "="); ok && k == key {
						return v
					}
				}
				return def
			}
			return "[mysqld]\ndatadir=" + get("MARIADB_DATA_DIR", "/opt/containers/data") +
				"\nmax_connections=" + get("MARIADB_MAX_CONNECTIONS", "200") + "\n", nil
		},
	}
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	got := read(t, target)
	want := "[mysqld]\ndatadir=/data\nmax_connections=300\n"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestProcessFuncUsesEditorBuildingBlocks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "pgbackrest.conf")
	write(t, target, "pg1-host=x\npg2-host=y\nbackup-standby=y\n")
	t.Setenv("NODE_ROLE", "primary")

	fp := domain.FileProvision{
		Path: target,
		ProcessFunc: func(editor domain.FileEditor, environ []string) error {
			if err := editor.Remove("^pg2-"); err != nil {
				return err
			}
			return editor.Upsert("^backup-standby=", "backup-standby=n")
		},
	}
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	got := read(t, target)
	want := "pg1-host=x\nbackup-standby=n\n"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestProcessFuncConditionalRemove(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config")
	write(t, target, "keep=1\nsecret=x\nkeep=2\n")
	t.Setenv("REMOVE_SECRET", "true")

	fp := domain.FileProvision{
		Path: target,
		ProcessFunc: func(editor domain.FileEditor, environ []string) error {
			if envHasValue(environ, "REMOVE_SECRET", "true") {
				return editor.Remove("^secret=")
			}
			return nil
		},
	}
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	got := read(t, target)
	want := "keep=1\nkeep=2\n"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func envHasValue(environ []string, name, value string) bool {
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name && v == value {
			return true
		}
	}
	return false
}

func TestFileEditorReadMerge(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config")
	write(t, target, "existing=1\n")

	fp := domain.FileProvision{
		Path: target,
		ContentFunc: func(editor domain.FileEditor, environ []string) (string, error) {
			existing, err := editor.Read()
			if err != nil {
				return "", err
			}
			return existing + "new=2\n", nil
		},
	}
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	got := read(t, target)
	want := "existing=1\nnew=2\n"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestValueFormatAutoQuotesStrings(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "postgresql.conf")
	t.Setenv("POSTGRESQL_CONFIG_SHARED_PRELOAD_LIBRARIES", "pg_stat_statements")
	t.Setenv("POSTGRESQL_CONFIG_MAX_CONNECTIONS", "100")

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
	if !strings.Contains(got, "max_connections = 100\n") {
		t.Fatalf("missing number line, content = %q", got)
	}
	if !strings.Contains(got, "shared_preload_libraries = 'pg_stat_statements'\n") {
		t.Fatalf("string value not quoted, content = %q", got)
	}
}
