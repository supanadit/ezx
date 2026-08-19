// Package logger defines the Port for structured, leveled logging. It is a
// thin module: log at a level, or check whether a level is enabled.
package logger

// Level enumerates log severity.
type Level string

const (
	// LevelDebug is the most verbose level.
	LevelDebug Level = "DEBUG"
	// LevelInfo is the default informational level.
	LevelInfo Level = "INFO"
	// LevelWarn reports recoverable issues.
	LevelWarn Level = "WARN"
	// LevelError reports failures.
	LevelError Level = "ERROR"
)

// Logger is the contract a logging adapter implements.
type Logger interface {
	Debug(format string, args ...any)
	Info(format string, args ...any)
	Warn(format string, args ...any)
	Error(format string, args ...any)
	// Enabled reports whether the given level would be emitted.
	Enabled(level Level) bool
}

// Service is a thin wrapper around a Logger, delegating all calls.
type Service struct {
	log Logger
}

// NewService returns a Service backed by the given Logger.
func NewService(log Logger) *Service {
	return &Service{log: log}
}

// Debug delegates to the wrapped Logger.
func (s *Service) Debug(format string, args ...any) { s.log.Debug(format, args...) }

// Info delegates to the wrapped Logger.
func (s *Service) Info(format string, args ...any) { s.log.Info(format, args...) }

// Warn delegates to the wrapped Logger.
func (s *Service) Warn(format string, args ...any) { s.log.Warn(format, args...) }

// Error delegates to the wrapped Logger.
func (s *Service) Error(format string, args ...any) { s.log.Error(format, args...) }

// Enabled delegates to the wrapped Logger.
func (s *Service) Enabled(level Level) bool { return s.log.Enabled(level) }
