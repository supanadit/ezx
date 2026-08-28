package domain

import "fmt"

// ValidateChain checks the canonical flat form (ProcessChain.Nodes) for
// correctness. It is a pure function — the orchestrator calls Normalized first
// and assumes the resulting DAG is valid, focusing purely on coordination.
//
// Checks:
//   - every node has a non-empty, unique Name
//   - every DependsOn references an existing node name (no self-deps)
//   - no node depends on an exec node (exec replaces PID 1, so a dependent
//     would never run)
//   - the graph is acyclic (DFS cycle detection; the error names the cycle)
//   - at most one exec node; an exec node may depend only on oneshot nodes
//     (the "init DAG → exec main" pattern)
//   - a oneshot node may not combine with Exec, Scheduler, Health, or
//     needParentReady (a oneshot is "ready" only when it exits 0)
//   - a node may not set both Children and DependsOn (or DependsOnEdges)
//     pre-normalization
//   - a node may not set both DependsOn and DependsOnEdges; every
//     DependsOnEdges waitFor must be "started" / "ready" / "exit"
func ValidateChain(chain ProcessChain) error {
	nodes := chain.Nodes
	if len(nodes) == 0 {
		return nil
	}

	// Names must be non-empty and unique; flag the Children+DependsOn mix.
	index := make(map[string]int, len(nodes))
	for i := range nodes {
		n := &nodes[i]
		if n.Name == "" {
			return fmt.Errorf("node at index %d has an empty name", i)
		}
		if _, dup := index[n.Name]; dup {
			return fmt.Errorf("duplicate node name %q", n.Name)
		}
		index[n.Name] = i
		if len(n.Children) > 0 && len(n.DependsOn) > 0 {
			return fmt.Errorf("node %q sets both Children and DependsOn", n.Name)
		}
		if len(n.Children) > 0 && len(n.DependsOnEdges) > 0 {
			return fmt.Errorf("node %q sets both Children and DependsOnEdges", n.Name)
		}
		// A file-backed log destination requires a target path (fail-fast).
		if lg := n.Process.Log; lg != nil {
			if (lg.Stdout == LogDestFile || lg.Stderr == LogDestFile) && lg.FilePath == "" {
				return fmt.Errorf("node %q: log destination %q requires filePath", n.Name, LogDestFile)
			}
		}
	}

	// Oneshot restrictions: a oneshot runs to completion, so it cannot combine
	// with Exec (exec replaces PID 1 and never returns), Scheduler (a scheduled
	// node is by definition not a one-shot), Health (no long-running process to
	// probe), or needParentReady (a oneshot is "ready" only when it exits 0).
	for i := range nodes {
		n := &nodes[i]
		if !n.Oneshot {
			continue
		}
		if n.Exec {
			return fmt.Errorf("oneshot node %q cannot combine Oneshot with Exec", n.Name)
		}
		if n.Scheduler != nil {
			return fmt.Errorf("oneshot node %q cannot combine Oneshot with Scheduler", n.Name)
		}
		if n.Health != nil {
			return fmt.Errorf("oneshot node %q cannot combine Oneshot with Health", n.Name)
		}
		if n.NeedParentReady {
			return fmt.Errorf("oneshot node %q cannot set needParentReady", n.Name)
		}
	}

	// Edge references must resolve, be non-self, and never point at an exec node.
	// These checks run over each node's canonical edges (from either DependsOn+
	// NeedParentReady or DependsOnEdges); canonicalEdges also rejects the
	// DependsOn+DependsOnEdges mix and unknown waitFor values.
	for i := range nodes {
		edges, err := nodes[i].canonicalEdges()
		if err != nil {
			return err
		}
		for _, dep := range edges {
			j, ok := index[dep.Name]
			if !ok {
				return fmt.Errorf("node %q depends on unknown node %q", nodes[i].Name, dep.Name)
			}
			if i == j {
				return fmt.Errorf("node %q depends on itself", nodes[i].Name)
			}
			if nodes[j].Exec {
				return fmt.Errorf("node %q depends on exec node %q", nodes[i].Name, dep.Name)
			}
		}
	}

	// Exec restrictions: at most one exec node. An exec node may have DependsOn,
	// but only on oneshot nodes (the "init DAG → exec main" pattern): a oneshot
	// dep exits 0 before the exec fires, so the exec becomes PID 1 only after
	// init completes. Deps are guaranteed to resolve (edge loop above).
	execCount := 0
	for i := range nodes {
		if !nodes[i].Exec {
			continue
		}
		execCount++
		edges, err := nodes[i].canonicalEdges()
		if err != nil {
			return err
		}
		for _, dep := range edges {
			j := index[dep.Name]
			if !nodes[j].Oneshot {
				return fmt.Errorf("exec node %q may only depend on oneshot nodes, but %q is not oneshot", nodes[i].Name, dep.Name)
			}
		}
	}
	if execCount > 1 {
		return fmt.Errorf("at most one exec node allowed, found %d", execCount)
	}

	// DFS cycle detection; on a back edge, name the cycle.
	const (
		visitNone = 0
		visitIn   = 1
		visitDone = 2
	)
	visit := make([]int, len(nodes))
	stack := make([]string, 0, len(nodes))
	var dfs func(i int) error
	dfs = func(i int) error {
		visit[i] = visitIn
		stack = append(stack, nodes[i].Name)
		edges, err := nodes[i].canonicalEdges()
		if err != nil {
			return err
		}
		for _, dep := range edges {
			j := index[dep.Name]
			switch visit[j] {
			case visitNone:
				if err := dfs(j); err != nil {
					return err
				}
			case visitIn:
				// Back edge: a cycle from nodes[j] through stack back to it.
				cycle := []string{nodes[j].Name}
				for k := len(stack) - 1; k >= 0 && stack[k] != nodes[j].Name; k-- {
					cycle = append(cycle, stack[k])
				}
				cycle = append(cycle, nodes[j].Name)
				return fmt.Errorf("dependency cycle detected: %v", cycle)
			}
		}
		stack = stack[:len(stack)-1]
		visit[i] = visitDone
		return nil
	}
	for i := range nodes {
		if visit[i] == visitNone {
			if err := dfs(i); err != nil {
				return err
			}
		}
	}
	return nil
}
