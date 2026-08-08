package system

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/envutil"
)

// envSource represents a resolved value source for an operation execution.
type envSource struct {
	// value is the interpolated content (env var value or literal).
	value string
	// captures holds regex capture groups from FromEnvPattern (index 1..n).
	captures []string
	// name is the (transformed) capture group 1, used for ${name}.
	name string
}

var (
	// envVarPattern matches ${VAR} references for environment lookup.
	envVarPattern = regexp.MustCompile(`\$\{([^}]+)\}`)
	// interpolationPattern matches ${value}, ${name}, and ${N} capture references.
	interpolationPattern = regexp.MustCompile(`\$\{(value|name|[0-9]+)\}`)
)

// provisionFiles applies each FileProvision to its target file.
func provisionFiles(files []domain.FileProvision) error {
	for _, fp := range files {
		if err := provisionFile(fp); err != nil {
			return fmt.Errorf("provision file %q: %w", fp.Path, err)
		}
	}
	return nil
}

// provisionFile applies a single FileProvision rule.
func provisionFile(fp domain.FileProvision) error {
	if fp.Path == "" {
		return errors.New("FileProvision.Path is required")
	}
	if !envConditionMet(fp.When, os.Environ()) {
		return nil
	}

	baseTarget := expandEnvVars(fp.Path, nil)

	// ProcessFunc takes full control of file processing.
	if fp.ProcessFunc != nil {
		editor := newFileEditor(baseTarget)
		if err := fp.ProcessFunc(editor, os.Environ()); err != nil {
			return err
		}
		return applyPermissionAndOwner(baseTarget, fp.Permission, fp.Owner)
	}

	// ContentFunc generates the entire file content.
	if fp.ContentFunc != nil {
		editor := newFileEditor(baseTarget)
		content, err := fp.ContentFunc(editor, os.Environ())
		if err != nil {
			return err
		}
		if err := ensureParentDir(baseTarget); err != nil {
			return err
		}
		if err := os.WriteFile(baseTarget, []byte(content), 0o644); err != nil {
			return err
		}
		return applyPermissionAndOwner(baseTarget, fp.Permission, fp.Owner)
	}

	for _, op := range fp.Operations {
		if !envConditionMet(op.When, os.Environ()) {
			continue
		}
		if op.FromEnv != "" && op.FromEnvPattern != "" {
			return errors.New("FromEnv and FromEnvPattern are mutually exclusive")
		}
		if err := applyOperation(op, fp.Path, fp.Permission, fp.Owner); err != nil {
			return err
		}
	}

	// For static paths (no per-source interpolation), apply Permission/Owner once.
	// FileOpCopy with ${value} paths applies them per-file inside applyOperation.
	if !strings.Contains(fp.Path, "${") {
		if err := applyPermissionAndOwner(baseTarget, fp.Permission, fp.Owner); err != nil {
			return err
		}
	}
	return nil
}

// applyPermissionAndOwner sets the file permission and ownership if specified.
func applyPermissionAndOwner(target string, permission os.FileMode, owner string) error {
	if permission != 0 {
		if err := os.Chmod(target, permission); err != nil {
			return err
		}
	}
	if owner != "" {
		if err := chown(target, owner); err != nil {
			return err
		}
	}
	return nil
}

// applyOperation dispatches a single operation, expanding FromEnv/FromEnvPattern into one or more executions.
func applyOperation(op domain.FileOperation, target string, permission os.FileMode, owner string) error {
	sources, err := resolveSources(op)
	if err != nil {
		return err
	}
	for _, src := range sources {
		// FileOpCopy resolves the destination Path per-source because it may reference
		// ${value} (e.g., copying to a filename derived from the env var value).
		opTarget := target
		if op.Type == domain.FileOpCopy {
			opTarget = interpolate(target, src)
			if err := ensureParentDir(opTarget); err != nil {
				return err
			}
			if err := executeOperation(op, opTarget, src); err != nil {
				return err
			}
			if permission != 0 {
				if err := os.Chmod(opTarget, permission); err != nil {
					return err
				}
			}
			if owner != "" {
				if err := chown(opTarget, owner); err != nil {
					return err
				}
			}
			continue
		}
		if err := executeOperation(op, opTarget, src); err != nil {
			return err
		}
	}
	return nil
}

// resolveSources returns the list of value sources for an operation: one for FromEnv,
// one per match for FromEnvPattern, or a single literal source.
func resolveSources(op domain.FileOperation) ([]envSource, error) {
	switch {
	case op.FromEnvPattern != "":
		matches, err := envutil.Enumerate(os.Environ(), op.FromEnvPattern)
		if err != nil {
			return nil, fmt.Errorf("invalid FromEnvPattern %q: %w", op.FromEnvPattern, err)
		}
		var sources []envSource
		for _, m := range matches {
			captured := m.Captures[1]
			var nameTransform string
			if op.NameTransformFunc != nil {
				nameTransform = op.NameTransformFunc(captured)
			} else {
				nameTransform = applyNameTransform(captured, op.NameTransform)
			}
			sources = append(sources, envSource{
				value:    m.Value,
				captures: m.Captures[1:],
				name:     nameTransform,
			})
		}
		return sources, nil
	case op.FromEnv != "":
		value, ok := os.LookupEnv(op.FromEnv)
		if !ok {
			return nil, nil
		}
		return []envSource{{value: value}}, nil
	default:
		return []envSource{{value: op.Value}}, nil
	}
}

// executeOperation runs one operation with a resolved source.
func executeOperation(op domain.FileOperation, target string, src envSource) error {
	switch op.Type {
	case domain.FileOpCopy:
		return opCopy(op, target, src)
	case domain.FileOpReplace:
		return opReplace(target, op, src)
	case domain.FileOpAppend:
		return opAppend(target, op, src)
	case domain.FileOpRemove:
		return opRemove(target, op, src)
	case domain.FileOpEnsure:
		return opEnsure(target, op, src)
	case domain.FileOpSetProperty:
		return opSetProperty(target, op, src)
	case domain.FileOpInsertBefore:
		return opInsert(target, op, src, false)
	case domain.FileOpInsertAfter:
		return opInsert(target, op, src, true)
	case domain.FileOpReplaceBlock:
		return opReplaceBlock(target, op, src)
	case domain.FileOpSetBlock:
		return opSetBlock(target, op, src)
	default:
		return fmt.Errorf("unsupported operation type %q", op.Type)
	}
}

// expandEnvVars substitutes ${VAR} and ${VAR:-default} references with environment
// values from the process environment. ${VAR} expands to the value or "" if unset;
// ${VAR:-default} expands to default when the variable is unset or empty, mirroring
// POSIX shell semantics. When src is non-nil, the special references ${value}, ${name},
// and ${N} resolve against the source instead (also supporting the :-default form).
func expandEnvVars(s string, src *envSource) string {
	return expandEnvVarsFrom(s, src, os.LookupEnv)
}

// expandEnvVarsFrom is expandEnvVars with an injectable lookup function, so the same
// interpolation can resolve against an environ slice instead of the process env.
func expandEnvVarsFrom(s string, src *envSource, lookup func(string) (string, bool)) string {
	return envVarPattern.ReplaceAllStringFunc(s, func(m string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(m, "${"), "}")
		def, hasDef := "", false
		if before, after, ok := strings.Cut(name, ":-"); ok {
			name, def, hasDef = before, after, true
		}
		if src != nil {
			switch name {
			case "value":
				if hasDef && src.value == "" {
					return def
				}
				return src.value
			case "name":
				if hasDef && src.name == "" {
					return def
				}
				return src.name
			}
			if idx, err := strconv.Atoi(name); err == nil && idx >= 1 && idx <= len(src.captures) {
				if hasDef && src.captures[idx-1] == "" {
					return def
				}
				return src.captures[idx-1]
			}
		}
		if v, ok := lookup(name); ok && v != "" {
			return v
		}
		if hasDef {
			return def
		}
		return ""
	})
}

// interpolate expands ${value}, ${name}, ${N} using the source, then ${VAR} from env.
// Used for regex patterns (Pattern, BlockEnd, Marker) where no value formatting applies.
func interpolate(s string, src envSource) string {
	return interpolateFormatted(s, src, domain.ValueFormatRaw, nil)
}

// interpolateFormatted expands placeholders, applying ValueTransformFunc to the value
// and ValueFormat to the ${value} substitution. Used for content templates.
func interpolateFormatted(s string, src envSource, format domain.ValueFormat, transform domain.ValueTransformFunc) string {
	s = interpolationPattern.ReplaceAllStringFunc(s, func(m string) string {
		switch m {
		case "${value}":
			v := src.value
			if transform != nil {
				v = transform(v)
			}
			return formatValue(v, format)
		case "${name}":
			return src.name
		}
		idx, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(m, "${"), "}"))
		if err != nil || idx < 1 || idx > len(src.captures) {
			return m
		}
		return src.captures[idx-1]
	})
	return expandEnvVars(s, &src)
}

// resolveContent returns the effective output content for a content-producing operation:
// the LineFunc output when set, otherwise the Value template interpolated with the
// operation's ValueTransformFunc and ValueFormat applied to the value portion.
func resolveContent(op domain.FileOperation, src envSource) string {
	if op.LineFunc != nil {
		value := src.value
		if op.ValueTransformFunc != nil {
			value = op.ValueTransformFunc(value)
		}
		return op.LineFunc(src.name, value)
	}
	return interpolateFormatted(op.Value, src, op.ValueFormat, op.ValueTransformFunc)
}

// formatValue applies ValueFormat to a value.
func formatValue(value string, format domain.ValueFormat) string {
	switch format {
	case domain.ValueFormatQuote:
		return "'" + value + "'"
	case domain.ValueFormatAuto:
		if autoFormatUnquoted(value) {
			return value
		}
		return "'" + value + "'"
	default:
		return value
	}
}

// autoFormatUnquoted reports whether a value should stay unquoted under ValueFormatAuto.
var autoFormatUnquotedPattern = regexp.MustCompile(`^(?:[0-9]+|[0-9]+(?:kB|MB|GB|TB|ms|s|min|h|d)|on|off|true|false)$`)

func autoFormatUnquoted(value string) bool {
	return autoFormatUnquotedPattern.MatchString(strings.TrimSpace(value))
}

// applyNameTransform applies the name transform to a captured name.
func applyNameTransform(name string, t domain.NameTransform) string {
	switch t {
	case domain.NameTransformLower:
		return strings.ToLower(name)
	case domain.NameTransformUpper:
		return strings.ToUpper(name)
	case domain.NameTransformSnakeToDot:
		return strings.ReplaceAll(strings.ToLower(name), "_", ".")
	case domain.NameTransformSnakeToCamel:
		return snakeToCamel(name)
	case domain.NameTransformSnakeToKebab:
		return strings.ReplaceAll(strings.ToLower(name), "_", "-")
	default:
		return name
	}
}

// snakeToCamel converts UPPER_SNAKE (or lower_snake) to camelCase:
// SHARED_BUFFERS → sharedBuffers, already_camel → alreadyCamel.
func snakeToCamel(name string) string {
	lower := strings.ToLower(name)
	parts := strings.Split(lower, "_")
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// envConditionMet reports whether an env condition is satisfied.
func envConditionMet(c domain.EnvCondition, environ []string) bool {
	if c.Name == "" {
		return true
	}
	if c.Value == "" {
		return envutil.IsSet(environ, c.Name)
	}
	return envutil.HasValue(environ, c.Name, c.Value)
}

// ensureParentDir creates the parent directory of path if it does not exist.
func ensureParentDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

// readLines reads a file into lines (stripping trailing newline). Missing file returns nil.
func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	content := string(data)
	if content == "" {
		return nil, nil
	}
	return strings.Split(strings.TrimRight(content, "\n"), "\n"), nil
}

// writeLines writes lines to a file joined with newlines.
func writeLines(path string, lines []string) error {
	data := []byte(strings.Join(lines, "\n"))
	if len(data) > 0 {
		data = append(data, '\n')
	}
	return os.WriteFile(path, data, 0o644)
}

// readContent reads the entire file content; missing file returns empty string.
func readContent(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// opReplace overwrites the entire file content.
func opReplace(target string, op domain.FileOperation, src envSource) error {
	content := interpolateFormatted(src.value, src, domain.ValueFormatRaw, op.ValueTransformFunc)
	if err := ensureParentDir(target); err != nil {
		return err
	}
	return os.WriteFile(target, []byte(content), 0o644)
}

// opAppend appends interpolated value(s) to the file.
func opAppend(target string, op domain.FileOperation, src envSource) error {
	value := resolveContent(op, src)
	if value == "" && op.FromEnv == "" && op.FromEnvPattern == "" {
		value = src.value
	}
	return appendContent(target, value)
}

// appendContent appends content to a file, ensuring a leading newline when needed.
func appendContent(target, content string) error {
	existing, err := readContent(target)
	if err != nil {
		return err
	}
	out := existing
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	if content != "" {
		out += content
		if !strings.HasSuffix(content, "\n") {
			out += "\n"
		}
	}
	return os.WriteFile(target, []byte(out), 0o644)
}

// opRemove removes all lines matching the interpolated Pattern.
func opRemove(target string, op domain.FileOperation, src envSource) error {
	pattern := interpolate(op.Pattern, src)
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("invalid Pattern %q: %w", pattern, err)
	}
	lines, err := readLines(target)
	if err != nil {
		return err
	}
	var kept []string
	for _, line := range lines {
		if !re.MatchString(line) {
			kept = append(kept, line)
		}
	}
	return writeLines(target, kept)
}

// opEnsure ensures an interpolated line exists; appends if not found.
func opEnsure(target string, op domain.FileOperation, src envSource) error {
	value := resolveContent(op, src)
	if value == "" {
		return nil
	}
	lines, err := readLines(target)
	if err != nil {
		return err
	}
	for _, line := range lines {
		if line == value {
			return nil
		}
	}
	return appendContent(target, value)
}

// opSetProperty removes lines matching Pattern, then appends Value.
func opSetProperty(target string, op domain.FileOperation, src envSource) error {
	pattern := interpolate(op.Pattern, src)
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("invalid Pattern %q: %w", pattern, err)
	}
	value := resolveContent(op, src)
	if value == "" {
		return nil
	}

	lines, err := readLines(target)
	if err != nil {
		return err
	}
	var kept []string
	for _, line := range lines {
		if !re.MatchString(line) {
			kept = append(kept, line)
		}
	}
	if err := writeLines(target, kept); err != nil {
		return err
	}
	return appendContent(target, value)
}

// opInsert inserts Value before (or after) the first line matching Pattern.
// Not idempotent — designed for run-once container-start scripts.
func opInsert(target string, op domain.FileOperation, src envSource, after bool) error {
	pattern := interpolate(op.Pattern, src)
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("invalid Pattern %q: %w", pattern, err)
	}
	value := resolveContent(op, src)
	if value == "" {
		return nil
	}
	insertLines := splitValueLines(value)

	lines, err := readLines(target)
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		return nil
	}
	for i, line := range lines {
		if re.MatchString(line) {
			var out []string
			if after {
				out = append(out, lines[:i+1]...)
				out = append(out, insertLines...)
				out = append(out, lines[i+1:]...)
			} else {
				out = append(out, lines[:i]...)
				out = append(out, insertLines...)
				out = append(out, lines[i:]...)
			}
			return writeLines(target, out)
		}
	}
	return nil
}

// splitValueLines splits a possibly multi-line value into lines, dropping the empty
// trailing element produced by a final newline.
func splitValueLines(value string) []string {
	lines := strings.Split(strings.TrimSuffix(value, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

// opReplaceBlock replaces the block from Pattern (start) to BlockEnd with Value.
// BlockEnd empty means single-line (start line replaced by Value).
func opReplaceBlock(target string, op domain.FileOperation, src envSource) error {
	content, err := readContent(target)
	if err != nil {
		return err
	}
	startPattern := interpolate(op.Pattern, src)
	startRe, err := regexp.Compile(startPattern)
	if err != nil {
		return fmt.Errorf("invalid Pattern %q: %w", startPattern, err)
	}
	value := resolveContent(op, src)

	loc := startRe.FindStringIndex(content)
	if loc == nil {
		return nil
	}

	var endIdx int
	if op.BlockEnd == "" {
		endIdx = loc[1]
	} else {
		endPattern := interpolate(op.BlockEnd, src)
		endRe, err := regexp.Compile(endPattern)
		if err != nil {
			return fmt.Errorf("invalid BlockEnd %q: %w", endPattern, err)
		}
		endLoc := endRe.FindStringIndex(content[loc[1]:])
		if endLoc == nil {
			return fmt.Errorf("block start %q found but BlockEnd %q not found", startPattern, endPattern)
		}
		endIdx = loc[1] + endLoc[1]
	}
	endIdx = consumeTrailingNewline(content, endIdx)

	var out strings.Builder
	out.WriteString(content[:loc[0]])
	out.WriteString(value)
	out.WriteString(content[endIdx:])
	return os.WriteFile(target, []byte(out.String()), 0o644)
}

// consumeTrailingNewline advances idx past a single trailing newline (CRLF-aware).
func consumeTrailingNewline(content string, idx int) int {
	if idx < len(content) && content[idx] == '\r' && idx+1 < len(content) && content[idx+1] == '\n' {
		return idx + 2
	}
	if idx < len(content) && content[idx] == '\n' {
		return idx + 1
	}
	return idx
}

// opSetBlock removes the block from Pattern to BlockEnd if it exists, then inserts Value
// before Marker (or appends if Marker not found). Idempotent.
func opSetBlock(target string, op domain.FileOperation, src envSource) error {
	content, err := readContent(target)
	if err != nil {
		return err
	}
	startPattern := interpolate(op.Pattern, src)
	startRe, err := regexp.Compile(startPattern)
	if err != nil {
		return fmt.Errorf("invalid Pattern %q: %w", startPattern, err)
	}
	value := resolveContent(op, src)
	if value == "" {
		return nil
	}

	// Remove existing block if present.
	loc := startRe.FindStringIndex(content)
	if loc != nil {
		var endIdx int
		if op.BlockEnd == "" {
			endIdx = loc[1]
		} else {
			endPattern := interpolate(op.BlockEnd, src)
			endRe, err := regexp.Compile(endPattern)
			if err != nil {
				return fmt.Errorf("invalid BlockEnd %q: %w", endPattern, err)
			}
			endLoc := endRe.FindStringIndex(content[loc[1]:])
			if endLoc == nil {
				return fmt.Errorf("block start %q found but BlockEnd %q not found", startPattern, endPattern)
			}
			endIdx = loc[1] + endLoc[1]
		}
		endIdx = consumeTrailingNewline(content, endIdx)
		content = content[:loc[0]] + content[endIdx:]
	}

	// Insert before Marker or append.
	if op.Marker == "" {
		return appendContent(target, value)
	}
	markerPattern := interpolate(op.Marker, src)
	markerRe, err := regexp.Compile(markerPattern)
	if err != nil {
		return fmt.Errorf("invalid Marker %q: %w", markerPattern, err)
	}
	if mLoc := markerRe.FindStringIndex(content); mLoc != nil {
		var out strings.Builder
		out.WriteString(content[:mLoc[0]])
		out.WriteString(value)
		if !strings.HasSuffix(value, "\n") {
			out.WriteString("\n")
		}
		out.WriteString(content[mLoc[0]:])
		return os.WriteFile(target, []byte(out.String()), 0o644)
	}
	return appendContent(target, value)
}

// opCopy copies a file from the interpolated Source to the target.
func opCopy(op domain.FileOperation, target string, src envSource) error {
	source := interpolate(op.Source, src)
	if source == "" {
		return errors.New("FileOpCopy requires Source")
	}
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open source %q: %w", source, err)
	}
	defer in.Close()

	if err := ensureParentDir(target); err != nil {
		return err
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, in); err != nil {
		return err
	}
	return os.WriteFile(target, buf.Bytes(), 0o644)
}

// chown changes file ownership from "user:group" or "user".
func chown(path, owner string) error {
	user, group, _ := strings.Cut(owner, ":")
	uid, err := lookupUID(user)
	if err != nil {
		return err
	}
	gid := -1
	if group != "" {
		gid, err = lookupGID(group)
		if err != nil {
			return err
		}
	}
	return os.Chown(path, uid, gid)
}

// lookupUID resolves a user name (or numeric UID) to a UID by parsing /etc/passwd.
func lookupUID(name string) (int, error) {
	if name == "" {
		return -1, nil
	}
	if uid, err := strconv.Atoi(name); err == nil {
		return uid, nil
	}
	uid, err := lookupIDFromFile("/etc/passwd", name, 0, 2)
	if err != nil {
		return -1, fmt.Errorf("unknown user %q: %w", name, err)
	}
	return uid, nil
}

// lookupGID resolves a group name (or numeric GID) to a GID by parsing /etc/group.
func lookupGID(name string) (int, error) {
	if name == "" {
		return -1, nil
	}
	if gid, err := strconv.Atoi(name); err == nil {
		return gid, nil
	}
	gid, err := lookupIDFromFile("/etc/group", name, 0, 2)
	if err != nil {
		return -1, fmt.Errorf("unknown group %q: %w", name, err)
	}
	return gid, nil
}

// lookupIDFromFile finds the numeric ID of a named entry in a colon-separated table
// (e.g., /etc/passwd, /etc/group). nameField is the field index for the name and
// idField the field index for the numeric ID.
func lookupIDFromFile(path, name string, nameField, idField int) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return -1, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) <= idField {
			continue
		}
		if fields[nameField] != name {
			continue
		}
		id, err := strconv.Atoi(fields[idField])
		if err != nil {
			return -1, err
		}
		return id, nil
	}
	return -1, os.ErrNotExist
}
