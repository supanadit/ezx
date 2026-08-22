package system

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/supanadit/ezx/logger"
)

// Logger implements the logger.Logger Port writing leveled text to an io.Writer
// (defaults to stderr), mirroring the containers logging.sh output format.
type Logger struct {
	out   io.Writer
	level logger.Level
}

// NewLogger returns a Logger writing to stderr at INFO level.
func NewLogger() *Logger {
	return &Logger{out: os.Stderr, level: logger.LevelInfo}
}

// SetLevel configures the minimum emitted level.
func (l *Logger) SetLevel(level logger.Level) {
	l.level = level
}

// SetOutput overrides the destination writer.
func (l *Logger) SetOutput(w io.Writer) {
	l.out = w
}

func (l *Logger) logf(level logger.Level, format string, args ...any) {
	if !l.Enabled(level) {
		return
	}
	// When no args are supplied, treat the message as a literal string rather
	// than a Sprintf format — otherwise configs containing "%" (e.g. a postgres
	// archive_command with "%p") render as "%!p(MISSING)" in logs.
	var line string
	if len(args) == 0 {
		line = format
	} else {
		line = fmt.Sprintf(format, args...)
	}
	if !strings.HasSuffix(line, "\n") {
		line += "\n"
	}
	fmt.Fprintf(l.out, "[%s] %s", level, line)
}

// Debug emits a debug-level message.
func (l *Logger) Debug(format string, args ...any) { l.logf(logger.LevelDebug, format, args...) }

// Info emits an info-level message.
func (l *Logger) Info(format string, args ...any) { l.logf(logger.LevelInfo, format, args...) }

// Warn emits a warn-level message.
func (l *Logger) Warn(format string, args ...any) { l.logf(logger.LevelWarn, format, args...) }

// Error emits an error-level message.
func (l *Logger) Error(format string, args ...any) { l.logf(logger.LevelError, format, args...) }

// Enabled reports whether the given level is emitted at the current threshold.
func (l *Logger) Enabled(level logger.Level) bool {
	order := map[logger.Level]int{
		logger.LevelDebug: 0,
		logger.LevelInfo:  1,
		logger.LevelWarn:  2,
		logger.LevelError: 3,
	}
	cur, ok := order[l.level]
	if !ok {
		cur = order[logger.LevelInfo]
	}
	return order[level] >= cur
}
