package script

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/supanadit/ezx/internal/repository"
)

// EnvModule exposes ezx.env: environment variable lookup helpers to scripts.
type EnvModule struct{}

// NewEnvModule returns an EnvModule.
func NewEnvModule() *EnvModule {
	return &EnvModule{}
}

// Get returns the value of an environment variable, or def if unset/empty.
func (m *EnvModule) Get(name string, def ...string) string {
	fallback := ""
	if len(def) > 0 {
		fallback = def[0]
	}
	return repository.Get(os.Environ(), name, fallback)
}

// Has reports whether an environment variable is set and non-empty.
func (m *EnvModule) Has(name string) bool {
	return repository.IsSet(os.Environ(), name)
}

// IsTruthy reports whether an environment variable is truthy. When the
// variable is unset or empty and a default is provided, the default determines
// the result — the shell default-true pattern (e.g. PGBACKREST_VERIFY default
// true). Without a default, unset/empty is falsy.
func (m *EnvModule) IsTruthy(name string, def ...string) bool {
	if v, ok := repository.Lookup(os.Environ(), name); ok && v != "" {
		return repository.IsTruthyValue(v)
	}
	if len(def) > 0 {
		return repository.IsTruthyValue(def[0])
	}
	return false
}

// NormalizeBool normalizes a raw value to "true", "false", or "".
func (m *EnvModule) NormalizeBool(value string) string {
	return repository.NormalizeBool(value)
}

// All returns a snapshot of the current environment as a map.
func (m *EnvModule) All() map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := splitKV(kv); ok {
			out[k] = v
		}
	}
	return out
}

func splitKV(kv string) (string, string, bool) {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			return kv[:i], kv[i+1:], true
		}
	}
	return "", "", false
}

// Int reads an environment variable and parses it as an integer. If the
// variable is unset/empty it returns def (throwing if no def is provided). A
// non-integer value fails fast with an error rather than silently defaulting.
func (m *EnvModule) Int(name string, def ...int) (int, error) {
	v, ok := repository.Lookup(os.Environ(), name)
	if !ok || v == "" {
		if len(def) > 0 {
			return def[0], nil
		}
		return 0, fmt.Errorf("env %q is not set", name)
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("env %q=%q is not an integer", name, v)
	}
	return n, nil
}

// Bool reads an environment variable with truthy semantics and returns a real
// Go bool (so JS sees true/false). When unset/empty and a default is provided,
// the default determines the result; otherwise unset/empty is falsy.
func (m *EnvModule) Bool(name string, def ...bool) bool {
	if v, ok := repository.Lookup(os.Environ(), name); ok && v != "" {
		return repository.IsTruthyValue(v)
	}
	if len(def) > 0 {
		return def[0]
	}
	return false
}

// List reads an environment variable, splits it on sep, trims each element and
// drops empties, returning the []string result. When unset/empty it returns def
// (defaulting to an empty slice).
func (m *EnvModule) List(name, sep string, def ...[]string) []string {
	v, ok := repository.Lookup(os.Environ(), name)
	if !ok || v == "" {
		if len(def) > 0 {
			return def[0]
		}
		return []string{}
	}
	var out []string
	for _, part := range strings.Split(v, sep) {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
