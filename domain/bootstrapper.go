package domain

// Bootstrapper represents a named bundle of processes for execution as a whole package.
// It encapsulates a process chain, providing a high-level interface for users to validate and run interdependent processes.
// Tradeoffs: Acts as a wrapper for ProcessChain to simplify user interaction (e.g., single Run() call),
// but keeps domain data-only; execution logic (parallel/sequential) deferred to future services.
type Bootstrapper struct {
	// Name is a user-friendly identifier for the bootstrapper package (e.g., "hesoly" for postgresql+pgbouncer+pgpool+etcd).
	Name string
	// Description provides a brief overview of the bootstrapper's purpose and components.
	Description string
	// Version is a semantic version string for the bootstrapper package (e.g., "1.0.0").
	Version string
	// Author is the creator or maintainer of the bootstrapper (e.g., "supanadit").
	Author string
	// Tags is a slice of keywords for categorization (e.g., ["database", "postgresql"]).
	Tags []string
	// Chain holds the underlying process dependency tree for spawning.
	Chain ProcessChain
}
