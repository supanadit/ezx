package script

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// This file adds the fluent, human-readable editor verbs on top of the
// low-level FileEditor primitives. They exist so container bootstrap scripts
// never hand-assemble config text (no `lines.push(...).join("\n")`, no manual
// `sed` regexes, no `.htaccess` splice/indexOf dances).
//
// The mental model is a document with a *scope*: the whole file, or a region
// inside it (a wrapped structural block or a comment-delimited section). Every
// verb mutates the file in place and returns the scope so calls chain:
//
//	conf := editor.open("/usr/local/apache2/conf/httpd.conf")
//	conf.property("DocumentRoot", "/var/www/html").property("User", "www-data")
//
//	rewrite := conf.wrap("<IfModule mod_rewrite.c>", "</IfModule>")
//	rewrite.property("RewriteEngine", "on")
//	rewrite.add("RewriteCond", "%{REQUEST_FILENAME} !-f")
//
//	wp := conf.section("wordpress", editor.sectionOpts{...}) // or plain name
//	wp.raw("<IfModule mod_rewrite.c>\n    RewriteEngine On\n</IfModule>")
//
// All verbs are idempotent: re-running converges to the same file.
//
// Design notes:
//   - property/add are position-preserving: they replace a matching line where
//     it already lives, or append inside the current scope. This is the crucial
//     difference from the low-level Upsert (set-property), which always appends
//     at the end of the whole file and corrupts position-sensitive files such as
//     httpd.conf (it can hoist an opening <IfModule> after its closing tag).
//   - wrap() returns a child scope bounded by its open/close lines; section()
//     returns a child scope bounded by begin/end markers that ezx writes.
//   - raw() writes verbatim text (content with no key/value shape).
//   - Each call reads the file, transforms it, and writes it back, so a crash
//     mid-script can never leave a half-built region.

// EditorOptions is the per-file profile passed to editor.open(path, opts):
// the separator between a key and its value, and the comment prefix used for
// section() markers when none is supplied.
type EditorOptions struct {
	// Separator sits between key and value in property()/add(). Defaults to a
	// single space (httpd.conf, .htaccess: "User www-data"). Use " = " for
	// postgresql.conf-style files, "=" for .properties, ": " for YAML-ish.
	Separator string
	// Comment is the comment prefix for section() markers. Defaults to "#".
	Comment string
}

func (o EditorOptions) withDefaults() EditorOptions {
	if o.Separator == "" {
		o.Separator = " "
	}
	if o.Comment == "" {
		o.Comment = "#"
	}
	return o
}

// scopedView is the scope state carried by a (possibly nested) editor handle.
// It is stored as a pointer inside FileEditor so the embedded methods can read
// it; the handle returned to scripts is a *FileEditor sharing the same inner.
type scopedView struct {
	// frame is the opening marker line of this scope ("" = whole file).
	frame string
	// closeMarker is the closing marker line for this scope ("" = none/root).
	closeMarker string
}

// readLines splits the file into lines (no trailing empty element).
func (e *FileEditor) readLines() ([]string, error) {
	content, err := e.inner.Read()
	if err != nil {
		return nil, err
	}
	if content == "" {
		return nil, nil
	}
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines, nil
}

// writeLines writes the lines with a single trailing newline.
func (e *FileEditor) writeLinesRaw(lines []string) error {
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	return e.inner.Replace(content)
}

// indexOfLine returns the first index whose trimmed line equals want, or -1.
func indexOfLine(lines []string, want string) int {
	for i, l := range lines {
		if strings.TrimSpace(l) == strings.TrimSpace(want) {
			return i
		}
	}
	return -1
}

// indexOfLineFrom is indexOfLine starting at from.
func indexOfLineFrom(lines []string, want string, from int) int {
	for i := from; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == strings.TrimSpace(want) {
			return i
		}
	}
	return -1
}

// scopeBounds returns the [start, end) line range of the current scope. start is
// the line after the scope's opening marker; end is the closing marker line.
// For the root scope start=0 and end=len(lines).
func (e *FileEditor) scopeBounds(lines []string) (int, int, error) {
	if e.view == nil || e.view.frame == "" {
		return 0, len(lines), nil
	}
	open := indexOfLine(lines, e.view.frame)
	if open < 0 {
		return 0, 0, fmt.Errorf("editor scope: opening marker %q not found", e.view.frame)
	}
	start := open + 1
	end := len(lines)
	if e.view.closeMarker != "" {
		closeIdx := indexOfLineFrom(lines, e.view.closeMarker, start)
		if closeIdx < 0 {
			return 0, 0, fmt.Errorf("editor scope: closing marker %q not found", e.view.closeMarker)
		}
		end = closeIdx
	}
	return start, end, nil
}

// child returns a new handle sharing this file's inner editor, scoped to the
// given frame.
func (e *FileEditor) child(frame, closeMarker string) *FileEditor {
	return &FileEditor{
		inner: e.inner,
		opts:  e.opts,
		view:  &scopedView{frame: frame, closeMarker: closeMarker},
	}
}

// Line replaces every line in the current scope matching the regex `pattern`
// with `replacement`, preserving position (the true in-place `sed -i
// 's/pattern/replacement/'`). If no line matches, `replacement` is appended at
// the end of the scope. This is the general primitive; Property is sugar over
// it for `key<sep>value` lines.
//
// It deliberately does NOT use Upsert (set-property), which removes the matching
// line and appends at the end of the whole file — correct for key=value configs
// but corrupting for block-structured files such as httpd.conf, where it can
// hoist an opening <IfModule>/<Directory> after its closing tag.
func (e *FileEditor) Line(pattern, replacement string) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("editor: invalid pattern %q: %w", pattern, err)
	}
	lines, err := e.readLines()
	if err != nil {
		return err
	}
	start, end, err := e.scopeBounds(lines)
	if err != nil {
		return err
	}
	for i := start; i < end; i++ {
		if re.MatchString(lines[i]) {
			lines[i] = replacement
			return e.writeLinesRaw(lines)
		}
	}
	return e.appendInScope(lines, start, end, replacement)
}

// Property sets a "key<sep>value" directive. It replaces the first matching
// line in the current scope in place, preserving its position among siblings;
// if no line matches, it appends the directive at the end of the scope. The
// match is anchored on the leading key token so `User` does not match `UserDir`.
func (e *FileEditor) Property(key, value string) error {
	return e.Line(keyLineRegexp(key, e.opts.Separator).String(), e.render(key, value))
}

// Add appends a "key<sep>value" directive inside the current scope if an
// identical line is not already present there. It is idempotent, so re-running a
// bootstrap does not accumulate duplicates. Use Repeat for directives that
// legitimately appear more than once with the same text.
func (e *FileEditor) Add(key, value string) error {
	line := e.render(key, value)
	lines, err := e.readLines()
	if err != nil {
		return err
	}
	start, end, err := e.scopeBounds(lines)
	if err != nil {
		return err
	}
	for i := start; i < end; i++ {
		if strings.TrimSpace(lines[i]) == strings.TrimSpace(line) {
			return nil
		}
	}
	return e.appendInScope(lines, start, end, line)
}

// Repeat unconditionally appends a "key<sep>value" directive inside the current
// scope. Unlike Add it never dedupes, for the rare directive that must appear
// multiple times with identical text.
func (e *FileEditor) Repeat(key, value string) error {
	lines, err := e.readLines()
	if err != nil {
		return err
	}
	start, end, err := e.scopeBounds(lines)
	if err != nil {
		return err
	}
	return e.appendInScope(lines, start, end, e.render(key, value))
}

// AddAll appends one directive line per value for a repeatable key.
func (e *FileEditor) AddAll(key string, values []string) error {
	for _, v := range values {
		if err := e.Add(key, v); err != nil {
			return err
		}
	}
	return nil
}

// Reset clears the body of the current scope, leaving the scope's own markers
// (or, for the root scope, the whole file) untouched. Use it to make a managed
// section or block fully owned by the script: call Reset() once, then build the
// body with property/add. This is what keeps a re-run idempotent when the body
// uses repeatable directives (e.g. Apache RewriteCond).
func (e *FileEditor) Reset() error {
	lines, err := e.readLines()
	if err != nil {
		return err
	}
	start, end, err := e.scopeBounds(lines)
	if err != nil {
		return err
	}
	out := append([]string{}, lines[:start]...)
	out = append(out, lines[end:]...)
	return e.writeLinesRaw(out)
}

// Remove deletes every line in the current scope matching the regex pattern.
func (e *FileEditor) RemoveScope(pattern string) error {
	lines, err := e.readLines()
	if err != nil {
		return err
	}
	start, end, err := e.scopeBounds(lines)
	if err != nil {
		return err
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("editor: invalid pattern %q: %w", pattern, err)
	}
	out := append([]string{}, lines[:start]...)
	for i := start; i < end; i++ {
		if !re.MatchString(lines[i]) {
			out = append(out, lines[i])
		}
	}
	out = append(out, lines[end:]...)
	return e.writeLinesRaw(out)
}

// Raw writes verbatim (possibly multi-line) text inside the current scope,
// replacing any previous content written by an earlier Raw call with the same
// marker. Used for content that has no key/value shape.
//
// Raw is idempotent per scope: on a bare handle it replaces the whole scope
// body; on a section()/wrap() handle it replaces that scope's body.
func (e *FileEditor) Raw(text string) error {
	lines, err := e.readLines()
	if err != nil {
		return err
	}
	start, end, err := e.scopeBounds(lines)
	if err != nil {
		return err
	}
	body := splitLines(text)
	out := append([]string{}, lines[:start]...)
	out = append(out, body...)
	out = append(out, lines[end:]...)
	return e.writeLinesRaw(out)
}

// Wrap opens (creating if absent) a structural block delimited by the given
// open/close lines, and returns a child scope bounded by them. The block is
// appended at the end of the current scope when it does not yet exist; when it
// does, the existing block is reused as-is.
//
//	rewrite := conf.wrap("<IfModule mod_rewrite.c>", "</IfModule>")
//	rewrite.property("RewriteEngine", "on")
func (e *FileEditor) Wrap(open, close string) (*FileEditor, error) {
	lines, err := e.readLines()
	if err != nil {
		return nil, err
	}
	start, end, err := e.scopeBounds(lines)
	if err != nil {
		return nil, err
	}
	if indexOfLineFrom(lines, open, start) < 0 || indexOfLineFrom(lines, close, start) < 0 {
		block := []string{open, close}
		out := append([]string{}, lines[:end]...)
		out = append(out, block...)
		out = append(out, lines[end:]...)
		if err := e.writeLinesRaw(out); err != nil {
			return nil, err
		}
	}
	return e.child(open, close), nil
}

// Section opens (creating if absent) a comment-delimited managed section and
// returns a child scope bounded by its BEGIN/END markers. The markers default to
// "<comment> BEGIN <name>" / "<comment> END <name>" (comment defaults to "#"),
// matching the WordPress convention; pass EditorOptions via the parent's Open
// call to change the prefix, or use SectionNamed to set the exact markers.
//
//	wp := conf.section("wordpress")          // # BEGIN wordpress / # END wordpress
//	wp := conf.sectionNamed("wordpress", "# BEGIN WordPress", "# END WordPress")
func (e *FileEditor) Section(name string) (*FileEditor, error) {
	begin := e.opts.Comment + " BEGIN " + name
	end := e.opts.Comment + " END " + name
	return e.SectionNamed(name, begin, end)
}

// SectionNamed is Section with explicit markers.
func (e *FileEditor) SectionNamed(_ string, begin, end string) (*FileEditor, error) {
	lines, err := e.readLines()
	if err != nil {
		return nil, err
	}
	_, endIdx, err := e.scopeBounds(lines)
	if err != nil {
		return nil, err
	}
	if indexOfLineFrom(lines, begin, 0) < 0 || indexOfLineFrom(lines, end, 0) < 0 {
		block := []string{begin, end}
		out := append([]string{}, lines[:endIdx]...)
		out = append(out, block...)
		out = append(out, lines[endIdx:]...)
		if err := e.writeLinesRaw(out); err != nil {
			return nil, err
		}
	}
	return e.child(begin, end), nil
}

// render builds the "key<sep>value" directive line. A value containing the
// separator's leading character is quoted when it holds spaces? No — verbatim,
// matching the original container behavior (directives are written as passed).
func (e *FileEditor) render(key, value string) string {
	if value == "" {
		return key
	}
	return key + e.opts.Separator + value
}

// appendInScope inserts a line at position end (just before the scope's closing
// marker, or at EOF for the root scope).
func (e *FileEditor) appendInScope(lines []string, _, end int, line string) error {
	out := append([]string{}, lines[:end]...)
	out = append(out, line)
	out = append(out, lines[end:]...)
	return e.writeLinesRaw(out)
}

// keyLineRegexp compiles a regexp that matches a directive line whose leading
// token is exactly `key` (followed by the separator or end of line).
func keyLineRegexp(key, separator string) *regexp.Regexp {
	escapedKey := regexp.QuoteMeta(key)
	sepClass := separatorClass(separator)
	// ^\s*<key>(\s*<sep>|\s*$)
	return regexp.MustCompile(`^\s*` + escapedKey + `(?:` + sepClass + `|\s*$)`)
}

// separatorClass returns a regexp fragment matching the separator (or leading
// whitespace). " " becomes `\s+`; " = " becomes `\s*=\s*`; "=" becomes `\s*=`.
func separatorClass(separator string) string {
	sep := strings.TrimSpace(separator)
	if sep == "" {
		return `\s+`
	}
	return `\s*` + regexp.QuoteMeta(sep) + `\s*`
}

// splitLines splits text into non-empty-preserving lines without a trailing
// empty element.
func splitLines(text string) []string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// FragmentOptions controls where a fragment is placed when it is first inserted
// (and re-inserted after removal).
//
// The zero value appends the fragment at the end of the current scope. Set After
// to insert it immediately after the first line matching a regex (e.g. after the
// opening "<?php" line of a PHP file).
//
// When both are empty the fragment is appended.
type FragmentOptions struct {
	// After is a regex; the fragment is inserted just after the first matching
	// line. Ignored when empty.
	After string
}

// Fragment inserts (or replaces) an include of a source file under a named,
// self-delimiting block, so PHP/other source never has to be written as a JS
// string. The source at path is read, fenced with
// "<comment> BEGIN ezx:<name>" / "<comment> END ezx:<name>", and placed at the
// anchor on first insert; on subsequent runs the existing fenced block is
// replaced in place. Calling Fragment with an empty path removes the block.
//
// This is the DX answer for shipping static application source (e.g. PHP) with a
// container: the code lives in a real .php file that the editor lints and
// highlights, and the script only says what to include and where.
//
//	wp.fragment("https", "/opt/container/php/https.php", { after: "^<\\?php" })
//	wp.fragment("loopback", "/opt/container/php/loopback.php")
//	wp.fragment("https", "")  // remove
func (e *FileEditor) Fragment(name, path string, opts ...FragmentOptions) error {
	begin := e.opts.Comment + " BEGIN ezx:" + name
	end := e.opts.Comment + " END ezx:" + name

	lines, err := e.readLines()
	if err != nil {
		return err
	}
	start, scopeEnd, err := e.scopeBounds(lines)
	if err != nil {
		return err
	}

	// Locate and remove any existing fenced block (anywhere in the file).
	bIdx := indexOfLine(lines, begin)
	eIdx := indexOfLine(lines, end)
	if bIdx >= 0 && eIdx >= bIdx {
		lines = append(append([]string{}, lines[:bIdx]...), lines[eIdx+1:]...)
		// Re-derive scope bounds after removal.
		start, scopeEnd, err = e.scopeBounds(lines)
		if err != nil {
			return err
		}
	}

	// Empty path means "remove only".
	if path == "" {
		return e.writeLinesRaw(lines)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("editor fragment %q: read %q: %w", name, path, err)
	}

	block := []string{begin}
	block = append(block, fragmentBody(string(content))...)
	block = append(block, end)

	o := FragmentOptions{}
	if len(opts) > 0 {
		o = opts[0]
	}

	insertAt := scopeEnd
	if o.After != "" {
		re, err := regexp.Compile(o.After)
		if err != nil {
			return fmt.Errorf("editor fragment %q: invalid After %q: %w", name, o.After, err)
		}
		if i := findMatch(lines, re, start, scopeEnd); i >= 0 {
			insertAt = i + 1
		}
	}

	out := append([]string{}, lines[:insertAt]...)
	out = append(out, block...)
	out = append(out, lines[insertAt:]...)
	return e.writeLinesRaw(out)
}

// fragmentBody prepares a source file's content for inclusion: it drops a
// leading "<?php" open tag (the fragment is included inside an already-open PHP
// file) and trims trailing blank lines. The source file stays a valid,
// lintable .php file; only the redundant open tag is removed at include time.
func fragmentBody(content string) []string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "<?php") {
		lines = lines[1:]
	}
	// Trim leading and trailing blank lines.
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// findMatch returns the first index in [start, end) matching re, or -1.
func findMatch(lines []string, re *regexp.Regexp, start, end int) int {
	for i := start; i < end; i++ {
		if re.MatchString(lines[i]) {
			return i
		}
	}
	return -1
}

// Define sets a PHP "define('NAME', value);" statement, in place. The value is
// typed like the original container: booleans and integers are written unquoted,
// array(...) / [...] literals are written verbatim, and everything else is
// single-quoted with the value escaped.
//
// It is a convenience over Line for the common PHP constant idiom, so scripts
// never build "define('...', '...');" strings by hand.
func (e *FileEditor) Define(name, value string) error {
	stmt := phpDefine(name, value)
	pattern := `^\s*define\s*\(\s*['"]` + regexp.QuoteMeta(name) + `['"]`
	return e.Line(pattern, stmt)
}

// phpDefine renders a single-line PHP define statement with type-aware quoting.
func phpDefine(name, value string) string {
	trimmed := strings.TrimSpace(value)
	if isPHPArrayLiteral(trimmed) || isPHPBoolOrInt(trimmed) {
		return "define('" + name + "', " + trimmed + ");"
	}
	return "define('" + name + "', '" + strings.ReplaceAll(value, "'", "\\'") + "');"
}

func isPHPArrayLiteral(v string) bool {
	return strings.HasPrefix(v, "[") || strings.HasPrefix(v, "array(")
}

func isPHPBoolOrInt(v string) bool {
	if v == "true" || v == "false" {
		return true
	}
	if v == "" {
		return false
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
