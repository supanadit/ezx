package domain

import (
	"os"
	"time"
)

// ProbeType enumerates the kind of readiness/health check to run.
type ProbeType string

const (
	// ProbeTypeExec runs a command and considers it ready when it exits 0.
	ProbeTypeExec ProbeType = "exec"
	// ProbeTypeTCP considers the probe ready when a TCP dial succeeds.
	ProbeTypeTCP ProbeType = "tcp"
	// ProbeTypeHTTP considers the probe ready when an HTTP request returns a
	// 2xx status (or a configured expected status).
	ProbeTypeHTTP ProbeType = "http"
)

// Probe describes a readiness or health check for a ProcessNode.
// It is the declarative counterpart of the bash wait_for_*_ready loops and
// healthcheck.sh checks.
type Probe struct {
	// Type selects which check runs (exec/tcp/http). Empty defaults to exec.
	Type ProbeType
	// Exec is the command (and args) to run for ProbeTypeExec.
	Exec []string
	// TCP holds the dial target for ProbeTypeTCP.
	TCP TCPProbe
	// HTTP holds the request target for ProbeTypeHTTP.
	HTTP HTTPProbe
	// Interval is how long to wait between attempts.
	Interval time.Duration
	// Timeout is the per-attempt timeout.
	Timeout time.Duration
	// MaxAttempts caps how many attempts before the probe fails.
	MaxAttempts int
}

// TCPProbe is the dial target for a TCP readiness check.
type TCPProbe struct {
	Host string
	Port int
}

// HTTPProbe is the request target for an HTTP readiness check.
type HTTPProbe struct {
	URL    string
	Method string
	// ExpectStatus, when non-zero, is the required status code. When zero, any
	// 2xx status is accepted.
	ExpectStatus int
}

// RestartMode enumerates the restart policy for a process.
type RestartMode string

const (
	// RestartNever never restarts a failed process (default).
	RestartNever RestartMode = "never"
	// RestartAlways restarts regardless of exit code.
	RestartAlways RestartMode = "always"
	// RestartOnFailure restarts only when the exit code is non-zero.
	RestartOnFailure RestartMode = "on-failure"
)

// RestartPolicy controls whether and how a failed process is restarted.
type RestartPolicy struct {
	// Mode selects the restart behavior.
	Mode RestartMode
	// MaxRetries caps consecutive restarts; <=0 means unlimited.
	MaxRetries int
	// Backoff is the initial delay between restarts.
	Backoff time.Duration
}

// LogDest enumerates log destinations for a process.
type LogDest string

const (
	// LogDestStdout writes process output to the parent's stdout.
	LogDestStdout LogDest = "stdout"
	// LogDestStderr writes process output to the parent's stderr.
	LogDestStderr LogDest = "stderr"
	// LogDestDiscard discards process output.
	LogDestDiscard LogDest = "discard"
)

// LogConfig controls how a spawned process's output is routed.
type LogConfig struct {
	// Stdout is the destination for the process's standard output.
	Stdout LogDest
	// Stderr is the destination for the process's standard error.
	Stderr LogDest
	// FilePath, when Stdout or Stderr is a file-backed destination, is the
	// target file path. Reserved for future per-process log files.
	FilePath string
}

// ShutdownConfig controls graceful shutdown of a ProcessNode.
type ShutdownConfig struct {
	// Signal is sent to the process to request a graceful stop. Defaults to
	// SIGTERM.
	Signal os.Signal
	// Timeout is how long to wait for a graceful exit before escalating.
	Timeout time.Duration
	// ForceKill, when true, sends SIGKILL after Timeout elapses.
	ForceKill bool
}
