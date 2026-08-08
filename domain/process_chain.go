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
