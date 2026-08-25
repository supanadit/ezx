package domain

// ProcessNode represents a node in the process dependency tree, with a process config and child dependencies.
// Children are spawned after this node (parallel among siblings, sequential if chained).
type ProcessNode struct {
	// Name is a unique identifier for the process (e.g., "postgresql").
	Name string
	// Optional, when true, marks this child as non-fatal: if it fails (exits
	// non-zero without restart, or errors), the error is logged but does NOT
	// cancel the parent tree or exit the container. Use for sidecars (pgbouncer,
	// pgbackrest, sshd, backups) whose failure should not take down the main
	// program. Default false (fatal — preserves current behavior).
	Optional bool
	// Process holds the configuration for the executable.
	Process Process
	// Files is a slice of FileProvision rules applied before this process starts
	// (optional; nil means no file provisioning). Env-to-file conversions run in order.
	Files []FileProvision
	// NeedParentReady indicates if this process requires its parent in the tree to be fully ready before starting (optional; defaults to false).
	NeedParentReady bool
	// Readiness is an optional readiness probe checked after Start. When
	// NeedParentReady is true, children wait until this probe passes (or Start
	// returns if no probe is set).
	Readiness *Probe
	// ReadinessFunc, when set, is a Go callback that reports whether the node is
	// ready. It overrides the declarative Readiness probe. Go-only (not
	// serializable); the script delivery layer binds it from a JS function via the
	// runtime invoker (the goja:"readiness" tag maps it from the script's
	// `readiness: () => ...` property). It must be short and non-blocking. Called
	// repeatedly until it returns true or the node's context is cancelled.
	ReadinessFunc func() bool `goja:"readiness"`
	// Restart controls whether and how this process is restarted on failure
	// (optional; nil means never restart).
	Restart *RestartPolicy
	// Shutdown controls graceful shutdown of this process (optional; nil means
	// SIGTERM, 30s timeout, force-kill enabled).
	Shutdown *ShutdownConfig
	// Exec, when true, replaces the current process image (PID 1) with this
	// node's process via syscall.Exec — the final, long-running entrypoint
	// process (e.g. the postgres server) becomes PID 1 for native signal
	// handling. It must be a leaf with no still-supervised siblings; the
	// orchestrator returns an error otherwise. Mutually exclusive with Health.
	Exec bool
	// Scheduler, when set, makes this node a scheduled (cron-driven) process:
	// the orchestrator runs the node's Process on each tick of Schedule rather
	// than once at start. It is supervised and drained like a regular process.
	// Mutually exclusive with Exec and Health (the orchestrator returns an
	// error otherwise).
	Scheduler *SchedulerConfig
	// ForwardSignals lists the signal names to relay from ezx (PID 1) to this
	// process's process group while it is supervised, e.g. ["SIGTERM",
	// "SIGINT", "SIGHUP", "SIGUSR1", "SIGUSR2", "SIGWINCH"]. Empty means only
	// the shutdown signal (from Shutdown) is sent on drain.
	ForwardSignals []string
	// Health, when set, starts a health/readiness HTTP server (e.g. /readyz)
	// for the duration of this node's supervision, gated on Health.ReadyProbe.
	// Mutually exclusive with Exec.
	Health *HealthConfig
	// OnStart is an optional callback invoked after the process has started.
	// Go-only (not serializable); the script delivery layer binds it from a JS
	// function. It must be short and non-blocking.
	OnStart func()
	// OnReady is an optional callback invoked after the node's readiness probe
	// passes. Go-only; see OnStart for the binding and concurrency notes.
	OnReady func()
	// OnExit is an optional callback invoked with the exit code after the
	// process exits. Go-only; see OnStart for the binding and concurrency notes.
	OnExit func(code int)
	// Children is a slice of dependent child processes (recursive for unlimited depth).
	Children []ProcessNode
}

// ProcessChain holds a collection of process dependency trees for chained/parallel spawning.
// It encapsulates root nodes, allowing multiple independent trees with unlimited nesting.
// Tradeoffs: Tree structure supports your JSON-like nesting and depth, but requires recursive logic for traversal/spawning;
// simpler for linear chains but powerful for complex hierarchies.
type ProcessChain struct {
	// Roots is a slice of root ProcessNodes, each representing an independent dependency tree.
	Roots []ProcessNode
}
