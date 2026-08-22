package scriptmodules

import (
	"fmt"
	"strings"

	"github.com/goccy/go-yaml"
)

// YamlModule exposes ezx.yaml: a generic YAML builder for scripts. Scripts
// construct a plain JS object tree (nested maps/arrays/scalars) and yaml.build
// serializes it to a human-readable YAML string, handling indentation, quoting
// (e.g. "on"/"off", numbers stay bare), and lists. This replaces fragile
// template-literal YAML (e.g. the old patroniYaml) with structured JS.
//
//	const { yaml } = require("ezx");
//	const doc = yaml.build({
//	  scope: "postgres-cluster",
//	  postgresql: { listen: "0.0.0.0:5432", parameters: { hot_standby: "on" } },
//	});
//	// -> scope: postgres-cluster
//	//    postgresql:
//	//      listen: 0.0.0.0:5432
//	//      parameters:
//	//        hot_standby: "on"
type YamlModule struct{}

// NewYamlModule returns a YamlModule. It is stateless.
func NewYamlModule() *YamlModule {
	return &YamlModule{}
}

// Build serializes a JS object tree to a YAML string. An optional second
// argument may carry { indent: N } to override the default 2-space indent.
func (m *YamlModule) Build(obj map[string]any, opts ...map[string]any) (string, error) {
	indent := 2
	if len(opts) > 0 && opts[0] != nil {
		if v, ok := opts[0]["indent"]; ok {
			if n, ok := v.(int); ok && n > 0 {
				indent = n
			}
		}
	}
	b, err := yaml.MarshalWithOptions(obj, yaml.Indent(indent), yaml.IndentSequence(true))
	if err != nil {
		return "", fmt.Errorf("yaml build: %w", err)
	}
	return strings.TrimRight(string(b), "\n"), nil
}
