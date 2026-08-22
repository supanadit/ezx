package scriptmodules

import (
	"strings"
	"testing"
)

func TestYamlBuild(t *testing.T) {
	m := NewYamlModule()

	out, err := m.Build(map[string]any{
		"scope": "postgres-cluster",
		"restapi": map[string]any{
			"listen": "0.0.0.0:8008",
		},
		"postgresql": map[string]any{
			"parameters": map[string]any{
				"hot_standby":  "on",
				"port":         5432,
				"archive_mode": "off",
				"flag":         true,
			},
			"pg_hba": []any{
				"local all postgres trust",
				"host all all 0.0.0.0/0 md5",
			},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, want := range []string{
		"scope: postgres-cluster",
		"restapi:",
		"  listen: 0.0.0.0:8008",
		"postgresql:",
		"  parameters:",
		`    hot_standby: "on"`,  // "on" is a YAML bool-ish word, must be quoted
		"    port: 5432",
		`    archive_mode: "off"`,
		"    flag: true",
		"  pg_hba:",
		"    - local all postgres trust",
		"    - host all all 0.0.0.0/0 md5",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

func TestYamlBuildIndent(t *testing.T) {
	m := NewYamlModule()
	out, err := m.Build(map[string]any{
		"a": map[string]any{"b": map[string]any{"c": 1}},
	}, map[string]any{"indent": 4})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(out, "    c: 1") {
		t.Errorf("4-space indent not applied\n---\n%s", out)
	}
}

func TestYamlBuildEmptyValue(t *testing.T) {
	m := NewYamlModule()
	out, err := m.Build(map[string]any{
		"empty": "",
		"none":  nil,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(out, `empty: ""`) {
		t.Errorf("empty string should be quoted\n---\n%s", out)
	}
}

// TestYamlBuildPercent verifies literal "%" in scalar values (e.g. a postgres
// archive_command with "%p") round-trips intact — the mangling seen in logs is
// a logger Sprintf artifact, not a yaml-build issue.
func TestYamlBuildPercent(t *testing.T) {
	m := NewYamlModule()
	out, err := m.Build(map[string]any{
		"archive_command": "env -u PGBACKREST_ENABLE pgbackrest archive-push %p",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(out, "archive_command: env -u PGBACKREST_ENABLE pgbackrest archive-push %p") {
		t.Errorf("percent mangled\n---\n%s", out)
	}
}
