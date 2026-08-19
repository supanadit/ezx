package domain

import "os"

// FileProvision represents a declarative, env-driven file provisioning rule.
// It converts environment variables into file content or file operations, eliminating
// the need for shell scripts that manipulate config files based on runtime conditions
// (e.g., the pgBackRest primary/standby config rewrite, PostgreSQL settings injection,
// or WordPress wp-config.php defines).
//
// Precedence: ProcessFunc takes full control; otherwise ContentFunc generates the file
// content; otherwise Operations execute in slice order. This mirrors how shell pipelines
// chain (grep -v | grep -v | ensure) but is declarative and testable.
//
// Tradeoffs: Designed as a pure data model matching Process/ProcessNode conventions;
// execution logic (reading env, matching patterns, writing files) is deferred to a
// service/repository. Function fields (callbacks) make this struct unsuitable for
// serialization to YAML/JSON — it is intended to be constructed in Go code.
type FileProvision struct {
	// Path is the target file path (required). May reference env vars via ${VAR} syntax.
	Path string
	// Operations is an ordered list of transformations applied to the file content.
	// For the simple "env value → file" case, use a single Replace operation with FromEnv.
	Operations []FileOperation
	// ContentFunc generates the entire file content from the environment, optionally
	// reading existing content via FileEditor for merging. When set, Operations are
	// ignored. The returned string is written to Path, then Permission/Owner apply.
	ContentFunc ContentFunc
	// ProcessFunc provides full custom file processing via FileEditor. When set,
	// Operations and ContentFunc are ignored. Permission/Owner apply after it runs.
	// Use for logic that cannot be expressed declaratively (PHP injection, stateful
	// config, block state machines, cross-file operations).
	ProcessFunc ProcessFunc
	// Permission is the file mode to set after writing (e.g., 0640); zero means preserve existing.
	Permission os.FileMode
	// Owner is the user:group to set (e.g., "postgres:postgres"); empty means preserve.
	Owner string
	// When is an optional env-var condition that gates whether this provision applies.
	// If empty, the provision always applies.
	When EnvCondition
	// CreateOnly, when true, skips the provision if the target file already
	// exists. Useful for generating default configs without clobbering
	// user-provided files (the `if [ ! -f "$file" ]` guard from entrypoint
	// scripts).
	CreateOnly bool
}

// FileOperation represents a single transformation applied to a text file.
// Operations are line-level except for block ops (ReplaceBlock, SetBlock) and
// FileOpCopy, which handle multi-line content natively.
//
// Precedence within an operation: LineFunc overrides the Value template;
// NameTransformFunc overrides NameTransform; ValueTransformFunc runs before ValueFormat.
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
	// ${value} interpolation resolved at execution time. Overridden by LineFunc when set.
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
	// NameTransformFunc overrides NameTransform for custom name transformation
	// (e.g., converting UPPER_SNAKE to PascalCase). Applied to the captured name.
	NameTransformFunc NameTransformFunc
	// ValueTransformFunc applies a custom transformation to the value before ValueFormat
	// (e.g., escaping, normalization, custom typing).
	ValueTransformFunc ValueTransformFunc
	// LineFunc generates the config line from the resolved name and value, overriding the
	// Value template. Useful for type-aware output (e.g., PHP define() with array/boolean
	// detection) that cannot be expressed with ValueFormat alone.
	LineFunc LineFunc
	// ValueFormat controls how ${value} is formatted during interpolation.
	ValueFormat ValueFormat
	// When is an optional per-operation env-var condition. If empty, always applies.
	When EnvCondition
}

// FileEditor exposes all file operations as methods, letting callback developers compose
// the same building blocks used by declarative Operations without reimplementing
// regex/line/block logic. Implementations wrap a target file.
type FileEditor interface {
	// Path returns the target file path.
	Path() string
	// Read returns the entire file content; empty string if the file is missing.
	Read() (string, error)
	// ReadLines returns the file as a slice of lines.
	ReadLines() ([]string, error)
	// WriteLines replaces the file content with the given lines.
	WriteLines(lines []string) error
	// Replace overwrites the entire file content. Creates the file if missing.
	Replace(content string) error
	// Append adds content at the end of the file.
	Append(content string) error
	// Remove removes all lines matching the regex pattern.
	Remove(pattern string) error
	// Ensure ensures a line exists; appends if not found.
	Ensure(line string) error
	// Upsert removes lines matching pattern, then appends value.
	Upsert(pattern, value string) error
	// InsertBefore inserts content before the first line matching pattern.
	InsertBefore(pattern, content string) error
	// InsertAfter inserts content after the first line matching pattern.
	InsertAfter(pattern, content string) error
	// ReplaceBlock replaces the block from startPattern to endPattern with value.
	ReplaceBlock(startPattern, endPattern, value string) error
	// SetBlock removes the block (startPattern→endPattern) if present, then inserts value
	// before marker (or appends if marker not found). Idempotent.
	SetBlock(startPattern, endPattern, marker, value string) error
}

// NameTransformFunc transforms a captured env var name before interpolation.
// Overrides NameTransform when set on a FileOperation.
type NameTransformFunc func(name string) string

// ValueTransformFunc transforms an env var value before ValueFormat is applied.
type ValueTransformFunc func(value string) string

// LineFunc generates a config line from a resolved name and value.
// Overrides the Value template when set on a FileOperation.
type LineFunc func(name, value string) string

// ContentFunc generates the entire file content from the environment.
// It may read existing content via FileEditor for merging. The returned string is
// written to the target path. Overrides Operations when set.
type ContentFunc func(editor FileEditor, environ []string) (string, error)

// ProcessFunc provides full custom file processing via FileEditor.
// It may read, modify, and write the target file (and secondary files) arbitrarily.
// Overrides Operations and ContentFunc when set. Permission/Owner apply after it runs.
type ProcessFunc func(editor FileEditor, environ []string) error

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
	// NameTransformSnakeToDot converts UPPER_SNAKE to lower.dot (e.g., Kafka/Flink config keys).
	NameTransformSnakeToDot NameTransform = "snake-to-dot"
	// NameTransformSnakeToCamel converts UPPER_SNAKE to camelCase (e.g., SHARED_BUFFERS → sharedBuffers).
	NameTransformSnakeToCamel NameTransform = "snake-to-camel"
	// NameTransformSnakeToKebab converts UPPER_SNAKE to kebab-case (e.g., SHARED_BUFFERS → shared-buffers).
	NameTransformSnakeToKebab NameTransform = "snake-to-kebab"
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
