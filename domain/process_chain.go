package domain

import "fmt"

// WaitMode is how a dependent waits for a dependency edge.
type WaitMode string

const (
	// WaitStarted waits for the dependency to have started (the default).
	WaitStarted WaitMode = "started"
	// WaitReady waits for the dependency's readiness phase to complete.
	WaitReady WaitMode = "ready"
	// WaitExit waits for the dependency to permanently exit 0 — a oneshot's
	// success gate, or a long-running dep that has stopped (rare; mainly for
	// oneshots).
	WaitExit WaitMode = "exit"
)

// Dependency is a single DAG edge with an explicit wait mode (the
// `dependsOnEdges` script form). It is the backward-compatible per-edge
// replacement for a `dependsOn` name plus the node-level `needParentReady`
// bool.
type Dependency struct {
	// Name is the dependency node's name.
	Name string `goja:"name"`
	// WaitFor is how this dependent waits for the edge ("started", "ready", or
	// "exit"; omitted defaults to "started").
	WaitFor WaitMode `goja:"waitFor"`
}

// Edge is the canonical internal form of a dependency edge. Normalized derives
// one per node from either DependsOn+NeedParentReady or DependsOnEdges; the
// orchestrator consumes only canonical edges.
type Edge struct {
	Name    string
	WaitFor WaitMode
}

// ProcessNode represents a node in the process dependency graph, with a process
// config and dependencies on other nodes. In the canonical (normalized) flat
// form a node's edges are expressed via DependsOn name references; Children is
// the legacy recursive-tree sugar that is desugared into DependsOn edges at
// bind time by Normalized.
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
	// runtime invoker (the goja:"readinessFunc" tag maps it from the script's
	// `readinessFunc: () => ...` property). It must be short and non-blocking.
	// Called repeatedly until it returns true or the node's context is cancelled.
	ReadinessFunc func() bool `goja:"readinessFunc"`
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
	// An exec node may depend on oneshot nodes (the "init DAG → exec main"
	// pattern): it fires only after all its oneshot deps exit 0 and no other
	// long-running node is still supervised.
	Exec bool
	// Oneshot, when true, runs the node's Process to completion instead of
	// supervising it as a long-running service. On exit code 0 the node
	// signals started+ready+exited and its dependents start; on non-zero it
	// fails (fatal unless Optional). Mutually exclusive with Exec, Scheduler,
	// and Health. May combine with Restart (retry the oneshot on failure).
	Oneshot bool
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
	// Children is the legacy recursive-tree sugar: a slice of dependent child
	// processes (recursive for unlimited depth). Children are desugared into
	// DependsOn edges by Normalized. A node must not set both Children and
	// DependsOn pre-normalization (validation error).
	Children []ProcessNode
	// DependsOn lists the names of nodes this node depends on (DAG edges, the
	// canonical form). Empty means the node starts as soon as the chain starts.
	// Edge semantics generalize the old parent/child behavior: the node starts
	// after all deps have started (or, when NeedParentReady is set, after all
	// deps are ready); when a dep exits permanently, dependents drain (running)
	// or are skipped (not yet started). The graph must be acyclic.
	DependsOn []string `goja:"dependsOn"`

	// DependsOnEdges lists dependencies with per-edge wait modes. Mutually
	// exclusive with DependsOn (a node sets one or the other; validated in
	// ValidateChain). Each edge's WaitFor may be "started" (default), "ready",
	// or "exit".
	DependsOnEdges []Dependency `goja:"dependsOnEdges"`

	// Edges is the canonical derived form of this node's dependency edges,
	// computed by Normalized from DependsOn+NeedParentReady or DependsOnEdges.
	// The orchestrator consumes only Edges. Internal derived field; hidden from
	// the script binding (goja:"-"), so scripts cannot bypass validation by
	// setting `edges` directly.
	Edges []Edge `goja:"-"`
}

// ProcessChain holds the nodes of a process dependency graph. The canonical
// flat form is Nodes (with explicit DependsOn edges); Roots/Children is the
// legacy recursive-tree form that Normalized desugars into Nodes. The
// orchestrator always works on the normalized flat form.
type ProcessChain struct {
	// Nodes is the canonical flat form: every node, in declaration order.
	// Edges are name references; the graph must be acyclic.
	Nodes []ProcessNode `goja:"nodes"`

	// Roots is the legacy tree form. Deprecated for direct construction:
	// Normalized flattens Roots/Children into Nodes. The script delivery layer
	// always normalizes before handing the chain to the orchestrator.
	Roots []ProcessNode
}

// Normalized returns the canonical flat form: Roots/Children (if set) are
// desugared into Nodes with dependency edges, and every node's canonical Edges
// are derived from either DependsOn+NeedParentReady or DependsOnEdges. Desugar
// rule for a child C of parent P:
//
//	C.DependsOn = append(C.DependsOn, P.Name)     (or DependsOnEdges if set)
//	C.NeedParentReady keeps its meaning, now applying to all of C's deps.
//
// A node that sets both Children and DependsOn (or DependsOnEdges) is rejected
// (ambiguous). The orchestrator consumes only the canonical Edges.
func (c ProcessChain) Normalized() (ProcessChain, error) {
	var nodes []ProcessNode
	if len(c.Nodes) > 0 {
		nodes = make([]ProcessNode, len(c.Nodes))
		copy(nodes, c.Nodes)
	} else {
		var walk func(n ProcessNode, parentName string) error
		walk = func(n ProcessNode, parentName string) error {
			if len(n.Children) > 0 && len(n.DependsOn) > 0 {
				return fmt.Errorf("node %q sets both Children and DependsOn", n.Name)
			}
			if len(n.Children) > 0 && len(n.DependsOnEdges) > 0 {
				return fmt.Errorf("node %q sets both Children and DependsOnEdges", n.Name)
			}
			children := n.Children
			if parentName != "" {
				if len(n.DependsOnEdges) > 0 {
					w := WaitStarted
					if n.NeedParentReady {
						w = WaitReady
					}
					n.DependsOnEdges = append(n.DependsOnEdges, Dependency{Name: parentName, WaitFor: w})
				} else {
					n.DependsOn = append(n.DependsOn, parentName)
				}
			}
			n.Children = nil // flat form carries edges only via DependsOn/DependsOnEdges
			nodes = append(nodes, n)
			for _, child := range children {
				if err := walk(child, n.Name); err != nil {
					return err
				}
			}
			return nil
		}
		for _, root := range c.Roots {
			if err := walk(root, ""); err != nil {
				return ProcessChain{}, err
			}
		}
	}
	// Derive the canonical edges for every node from its dependency form.
	for i := range nodes {
		edges, err := nodes[i].canonicalEdges()
		if err != nil {
			return ProcessChain{}, err
		}
		nodes[i].Edges = edges
	}
	return ProcessChain{Nodes: nodes}, nil
}

// canonicalEdges derives the canonical Edge list for a node from either
// DependsOn+NeedParentReady or DependsOnEdges. DependsOn and DependsOnEdges are
// mutually exclusive; an unknown waitFor is an error naming the node and edge.
// An omitted WaitFor defaults to WaitStarted.
func (n ProcessNode) canonicalEdges() ([]Edge, error) {
	if len(n.DependsOn) > 0 && len(n.DependsOnEdges) > 0 {
		return nil, fmt.Errorf("node %q sets both dependsOn and dependsOnEdges", n.Name)
	}
	if len(n.DependsOnEdges) > 0 {
		edges := make([]Edge, 0, len(n.DependsOnEdges))
		for _, d := range n.DependsOnEdges {
			w := d.WaitFor
			if w == "" {
				w = WaitStarted
			}
			if !isValidWaitMode(w) {
				return nil, fmt.Errorf("node %q: edge %q has unknown waitFor %q", n.Name, d.Name, d.WaitFor)
			}
			edges = append(edges, Edge{Name: d.Name, WaitFor: w})
		}
		return edges, nil
	}
	edges := make([]Edge, 0, len(n.DependsOn))
	for _, name := range n.DependsOn {
		w := WaitStarted
		if n.NeedParentReady {
			w = WaitReady
		}
		edges = append(edges, Edge{Name: name, WaitFor: w})
	}
	return edges, nil
}

func isValidWaitMode(w WaitMode) bool {
	switch w {
	case WaitStarted, WaitReady, WaitExit:
		return true
	}
	return false
}
