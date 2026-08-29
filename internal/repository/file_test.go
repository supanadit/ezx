package repository

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
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

func TestProvisionFile_CreateOnlySkipsExisting(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "conf")
	write(t, target, "user=provided\n")

	fp := domain.FileProvision{
		Path:       target,
		CreateOnly: true,
		Operations: []domain.FileOperation{
			{Type: domain.FileOpReplace, Value: "default-config"},
		},
	}
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "user=provided\n" {
		t.Fatalf("CreateOnly clobbered existing file: %q", string(got))
	}
}

func TestProvisionFile_CreateOnlyWritesWhenMissing(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "conf")

	fp := domain.FileProvision{
		Path:       target,
		CreateOnly: true,
		Operations: []domain.FileOperation{
			{Type: domain.FileOpReplace, Value: "default-config"},
		},
	}
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "default-config" {
		t.Fatalf("CreateOnly should write when missing, got %q", string(got))
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
			if HasValue(environ, "REMOVE_SECRET", "true") {
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

func TestProvisionFilesWrapper(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.conf")
	fp := domain.FileProvision{
		Path: target,
		Operations: []domain.FileOperation{
			{Type: domain.FileOpReplace, Value: "hello"},
		},
	}
	if err := ProvisionFiles([]domain.FileProvision{fp}); err != nil {
		t.Fatalf("ProvisionFiles: %v", err)
	}
	if got := read(t, target); got != "hello" {
		t.Fatalf("content = %q, want %q", got, "hello")
	}
}

func TestProvisionFiles_ErrorWrapsPath(t *testing.T) {
	fp := domain.FileProvision{
		Path: "",
		Operations: []domain.FileOperation{
			{Type: domain.FileOpReplace, Value: "x"},
		},
	}
	err := ProvisionFiles([]domain.FileProvision{fp})
	if err == nil {
		t.Fatal("expected error for empty path")
	}
	if !strings.Contains(err.Error(), "provision file") {
		t.Fatalf("error should wrap path, got %q", err)
	}
}

func TestOpAppend(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "conf")
	write(t, target, "first=1\n")

	fp := domain.FileProvision{
		Path: target,
		Operations: []domain.FileOperation{
			{Type: domain.FileOpAppend, Value: "second=2"},
		},
	}
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	got := read(t, target)
	want := "first=1\nsecond=2\n"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestOpAppend_FromEnv(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "conf")
	t.Setenv("APPEND_ME", "from-env")

	// The supported FromEnv form uses ${value} in the Value template.
	fp := domain.FileProvision{
		Path: target,
		Operations: []domain.FileOperation{
			{Type: domain.FileOpAppend, FromEnv: "APPEND_ME", Value: "${value}"},
		},
	}
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	if got := read(t, target); got != "from-env\n" {
		t.Fatalf("content = %q, want %q", got, "from-env\n")
	}
}

func TestOpEnsure_AppendsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "conf")
	write(t, target, "a=1\n")

	fp := domain.FileProvision{
		Path: target,
		Operations: []domain.FileOperation{
			{Type: domain.FileOpEnsure, Value: "b=2"},
		},
	}
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	got := read(t, target)
	want := "a=1\nb=2\n"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestOpEnsure_NoDuplicateWhenPresent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "conf")
	write(t, target, "a=1\nb=2\n")

	fp := domain.FileProvision{
		Path: target,
		Operations: []domain.FileOperation{
			{Type: domain.FileOpEnsure, Value: "b=2"},
		},
	}
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	got := read(t, target)
	want := "a=1\nb=2\n"
	if got != want {
		t.Fatalf("content = %q, want %q (no duplicate)", got, want)
	}
}

func TestOpReplaceBlock(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "conf")
	write(t, target, "start\nold-block\nend\nkeep\n")

	// BlockEnd is searched mid-string (after the start match), so it must be
	// unanchored — matching the existing SetBlock test convention.
	fp := domain.FileProvision{
		Path: target,
		Operations: []domain.FileOperation{
			{
				Type:     domain.FileOpReplaceBlock,
				Pattern:  "start",
				BlockEnd: "end",
				Value:    "start\nnew-block\nend",
			},
		},
	}
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	got := read(t, target)
	// The trailing newline after the block is consumed; the value has none.
	want := "start\nnew-block\nendkeep\n"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestOpReplaceBlock_SingleLine(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "conf")
	write(t, target, "old-line\nkeep\n")

	fp := domain.FileProvision{
		Path: target,
		Operations: []domain.FileOperation{
			{Type: domain.FileOpReplaceBlock, Pattern: "old-line", Value: "new-line"},
		},
	}
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	got := read(t, target)
	// The trailing newline after the matched line is consumed (consistent with
	// block replacement), so the value directly abuts the next line.
	want := "new-linekeep\n"
	if got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestOpReplaceBlock_NoMatchNoop(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "conf")
	write(t, target, "keep\n")

	fp := domain.FileProvision{
		Path: target,
		Operations: []domain.FileOperation{
			{Type: domain.FileOpReplaceBlock, Pattern: "^absent$", Value: "x"},
		},
	}
	if err := provisionFile(fp); err != nil {
		t.Fatalf("provisionFile: %v", err)
	}
	if got := read(t, target); got != "keep\n" {
		t.Fatalf("content = %q, want %q", got, "keep\n")
	}
}

func TestLookupNumericUIDGID(t *testing.T) {
	// Numeric UID/GID resolution does not touch /etc/passwd or /etc/group.
	if got, err := lookupUID("1000"); err != nil || got != 1000 {
		t.Fatalf("lookupUID(1000) = %d, %v; want 1000, nil", got, err)
	}
	if got, err := lookupGID("1000"); err != nil || got != 1000 {
		t.Fatalf("lookupGID(1000) = %d, %v; want 1000, nil", got, err)
	}
	if got, err := lookupUID(""); err != nil || got != -1 {
		t.Fatalf("lookupUID(\"\") = %d, %v; want -1, nil", got, err)
	}
	if got, err := lookupGID(""); err != nil || got != -1 {
		t.Fatalf("lookupGID(\"\") = %d, %v; want -1, nil", got, err)
	}
}

func TestLookupIDFromFile(t *testing.T) {
	dir := t.TempDir()
	passwd := filepath.Join(dir, "passwd")
	write(t, passwd, "# comment\nroot:x:0:0:root:/root:/bin/bash\napp:x:1001:1001::/home/app:/bin/sh\n")

	got, err := lookupIDFromFile(passwd, "app", 0, 2)
	if err != nil || got != 1001 {
		t.Fatalf("lookupIDFromFile(app) = %d, %v; want 1001, nil", got, err)
	}
	if _, err := lookupIDFromFile(passwd, "missing", 0, 2); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lookupIDFromFile(missing) err = %v, want os.ErrNotExist", err)
	}
}

func TestChownNumericOwner(t *testing.T) {
	// chown with a numeric owner:group resolves without touching /etc/passwd
	// or /etc/group, so it works in any environment. We only assert it does
	// not error for the current user's numeric UID (chowning to your own UID
	// is always permitted).
	dir := t.TempDir()
	target := filepath.Join(dir, "conf")
	write(t, target, "x\n")

	uid := os.Getuid()
	owner := strconv.Itoa(uid)
	if err := chown(target, owner); err != nil {
		t.Fatalf("chown(%q) to own uid: %v", owner, err)
	}
	// user:group form with numeric group.
	if err := chown(target, owner+":"+owner); err != nil {
		t.Fatalf("chown(%q) to own uid:gid: %v", owner+":"+owner, err)
	}
}

func TestFileEditorMethods(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "conf")
	write(t, target, "a=1\nb=2\n")

	ed := OpenFileEditor(target)
	if ed.Path() != target {
		t.Fatalf("Path() = %q, want %q", ed.Path(), target)
	}

	// ReadLines
	lines, err := ed.ReadLines()
	if err != nil || len(lines) != 2 || lines[0] != "a=1" {
		t.Fatalf("ReadLines = %v, %v; want [a=1 b=2], nil", lines, err)
	}

	// WriteLines
	if err := ed.WriteLines([]string{"x=1", "y=2"}); err != nil {
		t.Fatalf("WriteLines: %v", err)
	}
	if got := read(t, target); got != "x=1\ny=2\n" {
		t.Fatalf("after WriteLines content = %q", got)
	}

	// Replace
	if err := ed.Replace("replaced"); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if got := read(t, target); got != "replaced" {
		t.Fatalf("after Replace content = %q", got)
	}

	// Append
	if err := ed.Append("appended"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if got := read(t, target); got != "replaced\nappended\n" {
		t.Fatalf("after Append content = %q", got)
	}

	// Ensure (present -> noop, absent -> append)
	if err := ed.Ensure("appended"); err != nil {
		t.Fatalf("Ensure present: %v", err)
	}
	if err := ed.Ensure("newline"); err != nil {
		t.Fatalf("Ensure absent: %v", err)
	}
	if got := read(t, target); got != "replaced\nappended\nnewline\n" {
		t.Fatalf("after Ensure content = %q", got)
	}

	// Remove
	if err := ed.Remove("^appended$"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := read(t, target); got != "replaced\nnewline\n" {
		t.Fatalf("after Remove content = %q", got)
	}

	// Upsert
	if err := ed.Upsert("^newline$", "newline=2"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if got := read(t, target); got != "replaced\nnewline=2\n" {
		t.Fatalf("after Upsert content = %q", got)
	}

	// InsertBefore / InsertAfter
	if err := ed.InsertBefore("^newline=2$", "before"); err != nil {
		t.Fatalf("InsertBefore: %v", err)
	}
	if err := ed.InsertAfter("^newline=2$", "after"); err != nil {
		t.Fatalf("InsertAfter: %v", err)
	}
	if got := read(t, target); got != "replaced\nbefore\nnewline=2\nafter\n" {
		t.Fatalf("after InsertBefore/After content = %q", got)
	}

	// ReplaceBlock (BlockEnd searched mid-string, so unanchored)
	if err := ed.ReplaceBlock("newline=2", "after", "newline=3\nafter"); err != nil {
		t.Fatalf("ReplaceBlock: %v", err)
	}
	// The trailing newline after the block is consumed; the value has none.
	if got := read(t, target); got != "replaced\nbefore\nnewline=3\nafter" {
		t.Fatalf("after ReplaceBlock content = %q", got)
	}

	// SetBlock (idempotent)
	if err := ed.SetBlock("newline=3", "after", "before", "newline=4\nafter"); err != nil {
		t.Fatalf("SetBlock: %v", err)
	}
	if err := ed.SetBlock("newline=4", "after", "before", "newline=4\nafter"); err != nil {
		t.Fatalf("SetBlock second: %v", err)
	}
	if got := read(t, target); got != "replaced\nnewline=4\nafter\nbefore\n" {
		t.Fatalf("after SetBlock content = %q", got)
	}
}

func TestFileEditor_ReadMissingReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	ed := OpenFileEditor(filepath.Join(dir, "missing"))
	got, err := ed.Read()
	if err != nil || got != "" {
		t.Fatalf("Read() = %q, %v; want \"\", nil", got, err)
	}
	lines, err := ed.ReadLines()
	if err != nil || lines != nil {
		t.Fatalf("ReadLines() = %v, %v; want nil, nil", lines, err)
	}
}
