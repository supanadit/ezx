// Package scriptmodules provides the host modules exposed to user scripts
// under the "ezx" namespace, e.g. require("ezx/log"). Each module is a Go
// struct whose exported methods become the script-visible API.
package scriptmodules

import (
	"github.com/supanadit/ezx/logger"
)

// LogModule exposes ezx.log: debug/info/warn/error logging to scripts.
type LogModule struct {
	log logger.Logger
}

// NewLogModule returns a LogModule backed by the given Logger.
func NewLogModule(log logger.Logger) *LogModule {
	return &LogModule{log: log}
}

// Debug emits a debug-level message.
func (m *LogModule) Debug(format string, args ...any) { m.log.Debug(format, args...) }

// Info emits an info-level message.
func (m *LogModule) Info(format string, args ...any) { m.log.Info(format, args...) }

// Warn emits a warn-level message.
func (m *LogModule) Warn(format string, args ...any) { m.log.Warn(format, args...) }

// Error emits an error-level message.
func (m *LogModule) Error(format string, args ...any) { m.log.Error(format, args...) }

// Enabled reports whether the given level is emitted.
func (m *LogModule) Enabled(level logger.Level) bool { return m.log.Enabled(level) }
