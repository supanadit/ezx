package scriptmodules

import (
	"os"

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

// IsTruthy reports whether an environment variable is truthy.
func (m *EnvModule) IsTruthy(name string) bool {
	return repository.IsTruthy(os.Environ(), name)
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
