package script

import (
	"reflect"
	"strings"
	"testing"
	"unicode"

	"github.com/supanadit/ezx/domain"
)

// apiSurface describes one script-visible object and the exact method names it
// must expose. Method names are written as goja renders them: every exported
// Go method is lowercased via lowerName (FS→fs, TCP→tcp, SetReady→setReady),
// so scripts call `fs.ensureDir`, `probe.tcp`, `scheduler.every`, NOT the Go
// `EnsureDir`/`TCP`/`Every` forms.
//
// INTENTIONAL API CHANGE → update this list + bump the minor version. This is
// the lock on the public require("ezx") surface: a future refactor that renames
// or removes a binding (this already happened once: ReadinessFunc → readinessFunc)
// fails TestApiSurface with a clear message until the list below is updated.
var expectedAPISurface = []apiSurface{
	{name: "env", methods: []string{
		"get", "has", "isTruthy", "normalizeBool", "all", "int", "bool", "list",
	}},
	{name: "editor", methods: []string{
		"open",
	}},
	{name: "process", methods: []string{
		"spawn", "exec", "run", "capture", "shell",
	}},
	{name: "log", methods: []string{
		"debug", "info", "warn", "error", "enabled",
	}},
	{name: "chain", methods: []string{
		"run",
	}},
	{name: "fs", methods: []string{
		"stat", "exists", "mkdir", "mkdirAll", "chmod", "chmodRecursive",
		"chown", "chownRecursive", "readDir", "glob", "remove", "removeAll",
		"symlink", "realpath", "tempFile", "tempDir", "umask", "rename",
		"write", "ensureDir", "which",
	}},
	{name: "health", methods: []string{
		"setReady", "ready",
	}},
	{name: "probe", methods: []string{
		"tcp", "http", "exec", "disk", "process", "zombies",
	}},
	{name: "scheduler", methods: []string{
		"build", "parse", "trigger", "status", "every",
	}},
	{name: "api", methods: []string{
		"get", "post", "put", "delete",
	}},
	{name: "yaml", methods: []string{
		"build",
	}},
	{name: "config", methods: []string{
		"build", "builder",
	}},
	{name: "shell", methods: []string{
		"quote",
	}},
}

// apiSurface groups a script-visible object name with the exact lowercased
// method names it must expose. typ is the backing Go type whose exported
// methods become the script-visible methods.
type apiSurface struct {
	name    string
	methods []string
	typ     reflect.Type
}

// subObjectSurfaces are script-visible objects returned by module methods
// (editor.open, process.spawn, config.builder). They are part of the public
// require("ezx") surface, so they are locked down alongside the 13 modules.
// INTENTIONAL API CHANGE → update this list + bump the minor version.
var subObjectSurfaces = []apiSurface{
	{name: "editor.open() → FileEditor", typ: reflect.TypeOf(&FileEditor{}), methods: []string{
		"path", "read", "readLines", "writeLines", "replace", "append",
		"remove", "ensure", "upsert", "insertBefore", "insertAfter",
		"replaceBlock", "setBlock",
	}},
	{name: "process.spawn() → ProcessHandle", typ: reflect.TypeOf(&ProcessHandle{}), methods: []string{
		"start", "wait", "signal", "kill", "pid",
	}},
	{name: "config.builder() → ConfigBuilder", typ: reflect.TypeOf(&ConfigBuilder{}), methods: []string{
		"withSeparator", "withValueFormat", "withOptions", "comment", "blank",
		"set", "setIf", "setFlag", "setFromEnv", "section", "rows", "build",
	}},
	{name: "config.builder().section() → ConfigSection", typ: reflect.TypeOf(&ConfigSection{}), methods: []string{
		"set", "setIf", "setFlag", "setFromEnv",
	}},
}

// TestApiSurface locks down the script-visible require("ezx") API surface. It
// reflects over EzxModule's struct fields (module names from their goja tags)
// and over each module's exported methods (lowercased, matching the goja field
// mapper in internal/repository/js/helper.go). If any expected binding is
// removed or renamed, the test fails with a clear message.
func TestApiSurface(t *testing.T) {
	// Resolve each sub-module's backing type from EzxModule's fields.
	ezxT := reflect.TypeOf(&EzxModule{}).Elem()
	moduleTypes := make(map[string]reflect.Type, ezxT.NumField())
	for i := 0; i < ezxT.NumField(); i++ {
		f := ezxT.Field(i)
		name := f.Tag.Get("goja")
		if name == "" {
			name = lowerName(f.Name)
		}
		moduleTypes[name] = f.Type
	}

	// Every module in the expected surface must exist and expose its methods.
	for _, want := range expectedAPISurface {
		typ, ok := moduleTypes[want.name]
		if !ok {
			t.Errorf("module %q: missing — require(%q) would expose no such sub-module", want.name, "ezx")
			continue
		}
		assertMethods(t, "module "+want.name, typ, want.methods)
	}

	// Sub-objects returned by module methods must expose their methods too.
	for _, want := range subObjectSurfaces {
		assertMethods(t, want.name, want.typ, want.methods)
	}
}

// assertMethods verifies that every name in want is present as a lowercased
// exported method on typ.
func assertMethods(t *testing.T, label string, typ reflect.Type, want []string) {
	t.Helper()
	have := make(map[string]bool, typ.NumMethod())
	for i := 0; i < typ.NumMethod(); i++ {
		have[lowerName(typ.Method(i).Name)] = true
	}
	for _, m := range want {
		if !have[m] {
			t.Errorf("%s: missing or renamed method %q", label, m)
		}
	}
}

// logConfigFields are the script-visible field names of the `log` object on a
// process node (domain.LogConfig), as goja's field mapper renders them.
//
// INTENTIONAL API CHANGE → this is the additive per-service logging surface
// (0.3.0): `file` destination + `filePath`/`maxBytes`/`maxBackups`. Adding a
// field here is additive and non-breaking; removing/renaming one fails this
// test until the list is updated.
var logConfigFields = []string{
	"stdout", "stderr", "filePath", "maxBytes", "maxBackups",
}

// TestLogConfigFieldSurface locks down the script-visible `log` object fields
// on a process node. It reflects over domain.LogConfig and asserts the exact
// lowercased field names goja exposes to scripts.
func TestLogConfigFieldSurface(t *testing.T) {
	typ := reflect.TypeOf(domain.LogConfig{})
	have := make(map[string]bool, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		have[lowerName(typ.Field(i).Name)] = true
	}
	for _, f := range logConfigFields {
		if !have[f] {
			t.Errorf("log config: missing or renamed field %q", f)
		}
	}
}

// lowerName mirrors internal/repository/js.helper.lowerName exactly so the test
// asserts the same script-visible names goja produces: lower-casing a leading
// acronym (FS→fs, TCP→tcp) or the first letter of a camelCase name.
func lowerName(s string) string {
	if s == "" {
		return s
	}
	i := 0
	for i < len(s) && s[i] >= 'A' && s[i] <= 'Z' {
		i++
	}
	if i == len(s) {
		return strings.ToLower(s)
	}
	if i > 1 && i < len(s) && s[i] >= 'a' && s[i] <= 'z' {
		i--
	}
	return string(unicode.ToLower(rune(s[0]))) + s[1:]
}

// processNodeFields are the script-visible field names of a process node
// (domain.ProcessNode), as goja's field mapper renders them.
//
// INTENTIONAL API CHANGE → this is the additive per-node surface (0.3.0):
// `oneshot` (run-to-completion services). Adding a field here is additive and
// non-breaking; removing/renaming one fails this test until the list is updated.
var processNodeFields = []string{
	"name", "optional", "process", "files", "needParentReady", "readiness",
	"readinessFunc", "restart", "shutdown", "exec", "oneshot", "scheduler",
	"forwardSignals", "health", "onStart", "onReady", "onExit", "children",
	"dependsOn", "dependsOnEdges",
}

// TestProcessNodeFieldSurface locks down the script-visible process-node fields
// (the `nodes` entries passed to chain.run). It reflects over domain.ProcessNode
// and asserts the exact lowercased field names goja exposes to scripts.
func TestProcessNodeFieldSurface(t *testing.T) {
	typ := reflect.TypeOf(domain.ProcessNode{})
	have := make(map[string]bool, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		name := f.Tag.Get("goja")
		if name == "" {
			name = lowerName(f.Name)
		}
		have[name] = true
	}
	for _, f := range processNodeFields {
		if !have[f] {
			t.Errorf("process node: missing or renamed field %q", f)
		}
	}
}
