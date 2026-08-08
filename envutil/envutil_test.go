package envutil

import (
	"reflect"
	"testing"
)

func TestLookup(t *testing.T) {
	env := []string{"A=1", "EMPTY=", "B=two"}
	if v, ok := Lookup(env, "A"); !ok || v != "1" {
		t.Fatalf("Lookup(A) = %q, %v; want %q, true", v, ok, "1")
	}
	if v, ok := Lookup(env, "EMPTY"); !ok || v != "" {
		t.Fatalf("Lookup(EMPTY) = %q, %v; want empty, true", v, ok)
	}
	if v, ok := Lookup(env, "MISSING"); ok || v != "" {
		t.Fatalf("Lookup(MISSING) = %q, %v; want empty, false", v, ok)
	}
}

func TestGet(t *testing.T) {
	env := []string{"A=1", "EMPTY="}
	if got := Get(env, "A", "def"); got != "1" {
		t.Fatalf("Get(A) = %q, want %q", got, "1")
	}
	if got := Get(env, "MISSING", "def"); got != "def" {
		t.Fatalf("Get(MISSING) = %q, want %q", got, "def")
	}
	if got := Get(env, "EMPTY", "def"); got != "def" {
		t.Fatalf("Get(EMPTY) = %q, want %q (empty treated as unset)", got, "def")
	}
}

func TestIsSet(t *testing.T) {
	env := []string{"A=1", "EMPTY="}
	if !IsSet(env, "A") {
		t.Fatal("IsSet(A) = false, want true")
	}
	if IsSet(env, "EMPTY") {
		t.Fatal("IsSet(EMPTY) = true, want false")
	}
	if IsSet(env, "MISSING") {
		t.Fatal("IsSet(MISSING) = true, want false")
	}
}

func TestHasValue(t *testing.T) {
	env := []string{"ROLE=primary", "FLAG=true"}
	if !HasValue(env, "ROLE", "primary") {
		t.Fatal("HasValue(ROLE, primary) = false, want true")
	}
	if HasValue(env, "ROLE", "replica") {
		t.Fatal("HasValue(ROLE, replica) = true, want false")
	}
	if HasValue(env, "MISSING", "x") {
		t.Fatal("HasValue(MISSING, x) = true, want false")
	}
}

func TestIsTruthy(t *testing.T) {
	truthy := []string{"true", "1", "yes", "on", "y", "TRUE", "Yes", "ON", " Y "}
	for _, v := range truthy {
		env := []string{"F=" + v}
		if !IsTruthy(env, "F") {
			t.Fatalf("IsTruthy(%q) = false, want true", v)
		}
	}
	falsy := []string{"false", "0", "no", "off", "n", "other", "", "FALSE", "No"}
	for _, v := range falsy {
		env := []string{"F=" + v}
		if IsTruthy(env, "F") {
			t.Fatalf("IsTruthy(%q) = true, want false", v)
		}
	}
	if IsTruthy(nil, "MISSING") {
		t.Fatal("IsTruthy(MISSING) = true, want false")
	}
}

func TestIsFalsy(t *testing.T) {
	if !IsFalsy(nil, "MISSING") {
		t.Fatal("IsFalsy(MISSING) = false, want true")
	}
	if IsFalsy([]string{"F=true"}, "F") {
		t.Fatal("IsFalsy(true) = true, want false")
	}
	if !IsFalsy([]string{"F=false"}, "F") {
		t.Fatal("IsFalsy(false) = false, want true")
	}
}

func TestIsTruthyValue(t *testing.T) {
	if !IsTruthyValue("yes") || !IsTruthyValue("Y") || !IsTruthyValue("1") {
		t.Fatal("truthy values not detected")
	}
	if IsTruthyValue("no") || IsTruthyValue("0") || IsTruthyValue("") {
		t.Fatal("falsy values detected as truthy")
	}
}

func TestIsFalsyValue(t *testing.T) {
	if !IsFalsyValue("") || !IsFalsyValue("off") {
		t.Fatal("falsy values not detected")
	}
	if IsFalsyValue("true") {
		t.Fatal("truthy value detected as falsy")
	}
}

func TestNormalizeBool(t *testing.T) {
	tests := []struct{ in, want string }{
		{"true", "true"}, {"1", "true"}, {"yes", "true"}, {"on", "true"},
		{"TRUE", "true"}, {"Yes", "true"},
		{"false", "false"}, {"0", "false"}, {"no", "false"}, {"off", "false"},
		{"FALSE", "false"},
		{"y", ""}, {"n", ""}, {"other", ""}, {"", ""},
	}
	for _, tt := range tests {
		if got := NormalizeBool(tt.in); got != tt.want {
			t.Fatalf("NormalizeBool(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestEnumerate(t *testing.T) {
	env := []string{
		"POSTGRESQL_CONFIG_SHARED_BUFFERS=128MB",
		"POSTGRESQL_CONFIG_MAX_CONNECTIONS=100",
		"UNRELATED=x",
	}
	matches, err := Enumerate(env, `^POSTGRESQL_CONFIG_(.+)$`)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("Enumerate returned %d matches, want 2", len(matches))
	}
	wantNames := map[string]bool{"POSTGRESQL_CONFIG_SHARED_BUFFERS": true, "POSTGRESQL_CONFIG_MAX_CONNECTIONS": true}
	for _, m := range matches {
		if !wantNames[m.Name] {
			t.Fatalf("unexpected match name %q", m.Name)
		}
		if len(m.Captures) != 2 {
			t.Fatalf("captures for %q = %v, want 2 groups", m.Name, m.Captures)
		}
	}
}

func TestEnumerateNoMatches(t *testing.T) {
	env := []string{"FOO=1"}
	matches, err := Enumerate(env, `^BAR_`)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("Enumerate returned %d matches, want 0", len(matches))
	}
}

func TestEnumerateInvalidPattern(t *testing.T) {
	if _, err := Enumerate(nil, `(`); err == nil {
		t.Fatal("Enumerate with invalid pattern should return error")
	}
}

func TestMatchStructFields(t *testing.T) {
	env := []string{"CONFIG_MAX_LIMIT=10"}
	matches, err := Enumerate(env, `^CONFIG_(.+)$`)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
	m := matches[0]
	if m.Name != "CONFIG_MAX_LIMIT" || m.Value != "10" {
		t.Fatalf("match = %+v", m)
	}
	if !reflect.DeepEqual(m.Captures, []string{"CONFIG_MAX_LIMIT", "MAX_LIMIT"}) {
		t.Fatalf("captures = %v, want [CONFIG_MAX_LIMIT MAX_LIMIT]", m.Captures)
	}
}

func TestFilterExactNames(t *testing.T) {
	env := []string{"ETCD_NAME=n1", "ETCD_DATA_DIR=/data", "PATH=/usr/bin"}
	got, err := Filter(env, []string{"ETCD_NAME", "ETCD_DATA_DIR"}, nil)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	want := []string{"PATH=/usr/bin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Filter = %v, want %v", got, want)
	}
}

func TestFilterPatterns(t *testing.T) {
	env := []string{
		"PGBACKREST_REPO1_TYPE=s3",
		"PGBACKREST_STANZA=main",
		"POSTGRESQL_DB=app",
		"PATH=/usr/bin",
	}
	got, err := Filter(env, nil, []string{"^PGBACKREST_"})
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	want := []string{"POSTGRESQL_DB=app", "PATH=/usr/bin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Filter = %v, want %v", got, want)
	}
}

func TestFilterNamesAndPatterns(t *testing.T) {
	env := []string{
		"MINIO_DATA_DIR=/data",
		"MINIO_ROOT_USER=admin",
		"PGBACKREST_STANZA=main",
		"PATH=/usr/bin",
	}
	got, err := Filter(env, []string{"MINIO_DATA_DIR"}, []string{"^PGBACKREST_"})
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	want := []string{"MINIO_ROOT_USER=admin", "PATH=/usr/bin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Filter = %v, want %v", got, want)
	}
}

func TestFilterMinIOSelectiveKeepsCredentials(t *testing.T) {
	env := []string{
		"MINIO_DATA_DIR=/data",
		"MINIO_ADDRESS=:9000",
		"MINIO_CONSOLE_ADDRESS=:9001",
		"MINIO_ROOT_USER=admin",
		"MINIO_ROOT_PASSWORD=secret",
	}
	got, err := Filter(env, []string{
		"MINIO_DATA_DIR",
		"MINIO_ADDRESS",
		"MINIO_CONSOLE_ADDRESS",
		"MINIO_DISTRIBUTED_MODE_ENABLED",
		"MINIO_DISTRIBUTED_NODES",
	}, nil)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	want := []string{"MINIO_ROOT_USER=admin", "MINIO_ROOT_PASSWORD=secret"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Filter = %v, want %v", got, want)
	}
}

func TestFilterNoopWhenEmpty(t *testing.T) {
	env := []string{"A=1", "B=2"}
	got, err := Filter(env, nil, nil)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if !reflect.DeepEqual(got, env) {
		t.Fatalf("Filter = %v, want input unchanged %v", got, env)
	}
}

func TestFilterInputNotModified(t *testing.T) {
	env := []string{"A=1", "B=2"}
	original := []string{"A=1", "B=2"}
	if _, err := Filter(env, []string{"A"}, nil); err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if !reflect.DeepEqual(env, original) {
		t.Fatalf("Filter modified input: %v", env)
	}
}

func TestFilterInvalidPattern(t *testing.T) {
	if _, err := Filter([]string{"A=1"}, nil, []string{"("}); err == nil {
		t.Fatal("Filter with invalid pattern should return error")
	}
}
