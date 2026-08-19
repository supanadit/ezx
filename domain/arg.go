package domain

// ArgOperation declaratively builds zero or more CLI arguments from environment
// variables, mirroring the shell entrypoint pattern of assembling an ARG_LIST from
// env vars (prometheus --enable-feature flags, thanos --endpoint list expansion,
// pgBackRest --type/--target conditionals). It is the argument-side counterpart of
// FileOperation: operations run in slice order, appended after Arguments, and each
// may contribute zero or more arguments.
//
// Selection of which arguments are produced is controlled by FromEnv, FromEnvPattern,
// Split, When, and ConditionFunc; how each is formatted is controlled by Format, Flag,
// Value, NameTransform, NameTransformFunc, and ValueTransformFunc.
type ArgOperation struct {
	// Flag is the CLI flag prefix (e.g., "--endpoint", "--enable-feature", "-log.level").
	// Combined with the resolved value per Format. Not used for ArgFormatRaw.
	Flag string
	// Format controls how Flag and Value are assembled into the final argument(s).
	// Defaults to ArgFormatFlagValue when empty.
	Format ArgFormat
	// Value is a literal template with ${VAR}, ${VAR:-default}, ${value}, ${name}, and
	// ${N} interpolation. For IfSet ops it is the value paired with the env var; for
	// pattern-enum ops it is the per-match template. Ignored when ArgFormatBareFlag is set.
	Value string
	// FromEnv names an env var whose value drives the operation. Behavior depends on the
	// other fields:
	//   - no Split, ArgFormatBareFlag: adds Flag only if the var is truthy (if-truthy flag)
	//   - no Split, other formats: adds Flag+Value once if the var is non-empty (if-set value)
	//   - Split set: splits the var value by Split and adds one arg per element (list split)
	// Mutually exclusive with FromEnvPattern.
	FromEnv string
	// FromEnvPattern is a regex enumerating env vars; the operation repeats once per match
	// (pattern-enum, e.g., THANOS_RECEIVE_LABELS_* → --label). Capture groups are available
	// as ${1}, ${2}, ...; group 1 as ${name}. Mutually exclusive with FromEnv.
	FromEnvPattern string
	// Split is a literal delimiter used to split the FromEnv value into multiple elements,
	// each producing its own argument (e.g., "," for comma-separated lists, " " for
	// whitespace pass-through). Empty means no splitting. Multi-delimiter and nested splits
	// are handled in ArgsFunc.
	Split string
	// When gates whether this operation produces any arguments, based on an environment
	// variable (e.g., Name "PROMETHEUS_ENABLE_WEB_LIFECYCLE", Value "true" only adds the
	// flag when that var equals "true"). Empty Value means "any non-empty value". Ignored
	// when ConditionFunc is set.
	When EnvCondition
	// ConditionFunc is a custom predicate deciding whether this operation runs. It overrides
	// When when set. Use for gating that cannot be expressed declaratively (e.g., numeric
	// comparison: only add a flag when a value parses to > 0).
	ConditionFunc func(environ []string) bool
	// NameTransform transforms the captured name (FromEnvPattern capture group 1, or the
	// FromEnv var name) before interpolation into Value/Flag (e.g., lower to match shell
	// ${VAR,,}, snake-to-kebab for flag names).
	NameTransform NameTransform
	// NameTransformFunc overrides NameTransform for custom name transformation.
	NameTransformFunc NameTransformFunc
	// ValueTransformFunc applies a custom transformation to the resolved value before
	// formatting/interpolation (e.g., quoting, escaping, normalization).
	ValueTransformFunc ValueTransformFunc
}

// ArgFormat enumerates how a Flag and resolved Value are assembled into CLI arguments.
type ArgFormat string

const (
	// ArgFormatFlagValue emits "--flag=value" (single argument). Default.
	ArgFormatFlagValue ArgFormat = "flag-value"
	// ArgFormatFlagSpace emits "--flag" and "value" as two separate arguments.
	ArgFormatFlagSpace ArgFormat = "flag-space"
	// ArgFormatBareFlag emits "--flag" with no value (boolean toggle). The operation
	// contributes the flag only when the condition is met; Value is ignored.
	ArgFormatBareFlag ArgFormat = "bare-flag"
	// ArgFormatRaw emits only the value with no flag prefix (pass-through, e.g., splitting
	// GRAFANA_MIMIR_EXTRA_ARGS into bare args). Flag is ignored.
	ArgFormatRaw ArgFormat = "raw"
)

// ArgsFunc generates CLI arguments from the environment with full control. It overrides
// ArgOperations when set, and is concatenated after Arguments. It is the argument-side
// counterpart of ProcessFunc/ContentFunc for file provisioning, covering complex cases
// that cannot be expressed declaratively (component dispatch via a case statement,
// cross-argument logic, temp-file + --config-file hybrids).
type ArgsFunc func(environ []string) ([]string, error)
