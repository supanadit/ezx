package domain

import "os"

// FileProvision represents a declarative, env-driven file provisioning rule.
// It converts environment variables into file content or file operations, eliminating
// the need for shell scripts that manipulate config files based on runtime conditions
// (e.g., the pgBackRest primary/standby config rewrite, PostgreSQL settings injection,
// or WordPress wp-config.php defines).
//
// Tradeoffs: Designed as a pure data model matching Process/ProcessNode conventions;
// execution logic (reading env, matching patterns, writing files) is deferred to a
// service/repository. Operations execute in slice order — this mirrors how shell
// pipelines chain (grep -v | grep -v | ensure) but is declarative and testable.
type FileProvision struct {
	// Path is the target file path (required). May reference env vars via ${VAR} syntax.
	Path string
	// Operations is an ordered list of transformations applied to the file content.
	// For the simple "env value → file" case, use a single Replace operation with FromEnv.
	Operations []FileOperation
	// Permission is the file mode to set after writing (e.g., 0640); zero means preserve existing.
	Permission os.FileMode
	// Owner is the user:group to set (e.g., "postgres:postgres"); empty means preserve.
	Owner string
	// When is an optional env-var condition that gates whether this provision applies.
	// If empty, the provision always applies.
	When EnvCondition
}

// FileOperation represents a single transformation applied to a text file.
// Operations are line-level except for block ops (ReplaceBlock, SetBlock) and
// FileOpCopy, which handle multi-line content natively.
type FileOperation struct {
	// Type is the operation kind (see FileOperationType constants).
	Type FileOperationType
	// Pattern is a regex used by Remove, SetProperty, InsertBefore, InsertAfter,
	// ReplaceBlock (start marker), and SetBlock (start marker). Supports ${1}, ${name}
	// interpolation when FromEnvPattern is set.
	Pattern string
	// BlockEnd is the end regex for block ops (ReplaceBlock, SetBlock); empty means single-line.
	BlockEnd string
	// Marker is the fallback insertion point for SetBlock (InsertBefore this pattern);
	// empty means append to end of file when the block does not already exist.
	Marker string
	// Value is a literal string used by Replace, Append, Ensure, SetProperty, InsertBefore,
	// InsertAfter, ReplaceBlock, and SetBlock. Supports ${ENV_VAR}, ${1}, ${name}, and
	// ${value} interpolation resolved at execution time.
	Value string
	// FromEnv is an env var name whose value is used as ${value} (mutually exclusive with FromEnvPattern).
	// Convenience for the simple "write env var to file" Docker pattern.
	FromEnv string
	// FromEnvPattern is a regex to enumerate env vars; the operation repeats once per match
	// (mutually exclusive with FromEnv). Capture groups are available as ${1}, ${2}, ...
	FromEnvPattern string
	// Source is the source file path for FileOpCopy; supports ${value}, ${1} interpolation.
	Source string
	// NameTransform transforms the captured name (from FromEnvPattern capture group 1)
	// before interpolation into Pattern/Value (e.g., lower to match shell ${VAR,,}).
	NameTransform NameTransform
	// ValueFormat controls how ${value} is formatted during interpolation.
	ValueFormat ValueFormat
	// When is an optional per-operation env-var condition. If empty, always applies.
	When EnvCondition
}

// FileOperationType enumerates file transformation operations.
type FileOperationType string

const (
	// FileOpReplace replaces the entire file content with Value/FromEnv. Creates file if missing.
	FileOpReplace FileOperationType = "replace"
	// FileOpAppend appends Value/FromEnv as new line(s) at end of file.
	FileOpAppend FileOperationType = "append"
	// FileOpRemove removes all lines matching Pattern (regex).
	FileOpRemove FileOperationType = "remove"
	// FileOpEnsure ensures a line equal to Value exists; appends if not found.
	FileOpEnsure FileOperationType = "ensure"
	// FileOpSetProperty removes lines matching Pattern, then appends Value.
	// Composite of Remove + Ensure — the most common config-file mutation.
	FileOpSetProperty FileOperationType = "set-property"
	// FileOpInsertBefore inserts Value before the first line matching Pattern.
	// Not idempotent — designed for run-once container-start scripts.
	FileOpInsertBefore FileOperationType = "insert-before"
	// FileOpInsertAfter inserts Value after the first line matching Pattern.
	// Not idempotent — designed for run-once container-start scripts.
	FileOpInsertAfter FileOperationType = "insert-after"
	// FileOpReplaceBlock replaces the block from Pattern to BlockEnd (regex, greedy to first match)
	// with Value. Idempotent. BlockEnd empty means single-line.
	FileOpReplaceBlock FileOperationType = "replace-block"
	// FileOpSetBlock removes the block from Pattern to BlockEnd if it exists, then inserts Value
	// before Marker (or appends if Marker not found). Idempotent — the core PHP config injection op.
	FileOpSetBlock FileOperationType = "set-block"
	// FileOpCopy copies a file from Source to the FileProvision.Path. Bypasses content
	// transformation; Permission and Owner still apply.
	FileOpCopy FileOperationType = "copy"
)

// NameTransform transforms a captured env var name (e.g., SHARED_BUFFERS) before use.
type NameTransform string

const (
	// NameTransformNone leaves the captured name unchanged (default).
	NameTransformNone NameTransform = ""
	// NameTransformLower lowercases the captured name (matches shell ${VAR,,}).
	NameTransformLower NameTransform = "lower"
	// NameTransformUpper uppercases the captured name (matches shell ${VAR^^}).
	NameTransformUpper NameTransform = "upper"
)

// ValueFormat controls how ${value} is formatted during interpolation.
type ValueFormat string

const (
	// ValueFormatRaw uses the value as-is, no formatting (default). Use for multi-line blocks
	// that are already in the target syntax (e.g., a PHP array literal).
	ValueFormatRaw ValueFormat = ""
	// ValueFormatQuote always single-quotes the value: 'value'.
	ValueFormatQuote ValueFormat = "quote"
	// ValueFormatAuto smart-formats: numbers, booleans (on|off|true|false), and unit-suffixed
	// values are left unquoted; everything else is single-quoted. Covers most config files.
	ValueFormatAuto ValueFormat = "auto"
)

// EnvCondition gates an operation or provision based on an environment variable's value.
type EnvCondition struct {
	// Name is the environment variable name to check (required).
	Name string
	// Value is the required value for the condition to pass.
	// Empty means "any non-empty value satisfies this condition".
	Value string
}
