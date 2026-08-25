package script

import "strings"

// ShellModule exposes ezx.shell: shell quoting helpers. It complements
// process.shell for safely embedding values into explicit shell commands.
type ShellModule struct{}

// NewShellModule returns a ShellModule.
func NewShellModule() *ShellModule {
	return &ShellModule{}
}

// Quote single-quote-escapes a value for safe inclusion in a shell command. It
// wraps the value in single quotes and escapes any embedded single quote as
// '\'' (the standard transform). Returns the quoted string.
func (m *ShellModule) Quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
