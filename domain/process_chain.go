package domain

// ProcessNode represents a node in the process dependency tree, with a process config and child dependencies.
// Children are spawned after this node (parallel among siblings, sequential if chained).
type ProcessNode struct {
	// Name is a unique identifier for the process (e.g., "postgresql").
	Name string
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
	// ForwardSignals lists the signal names to relay from ezx (PID 1) to this
	// process's process group while it is supervised, e.g. ["SIGTERM",
	// "SIGINT", "SIGHUP", "SIGUSR1", "SIGUSR2", "SIGWINCH"]. Empty means only
	// the shutdown signal (from Shutdown) is sent on drain.
	ForwardSignals []string
	// Health, when set, starts a health/readiness HTTP server (e.g. /readyz)
	// for the duration of this node's supervision, gated on Health.ReadyProbe.
	// Mutually exclusive with Exec.
	Health *HealthConfig
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
