package domain

import (
	"reflect"
	"strings"
	"testing"
)

// TestNormalizedTreeToFlat verifies that the legacy Roots/Children tree form
// desugars into the canonical flat Nodes/DependsOn form with correct edges.
func TestNormalizedTreeToFlat(t *testing.T) {
	chain := ProcessChain{Roots: []ProcessNode{
		{
			Name: "postgres",
			Children: []ProcessNode{
				{Name: "pgbouncer", NeedParentReady: true, Children: []ProcessNode{
					{Name: "pgpool", NeedParentReady: true},
				}},
				{Name: "etcd"},
			},
		},
	}}

	norm, err := chain.Normalized()
	if err != nil {
		t.Fatalf("Normalized() error: %v", err)
	}
	if len(norm.Nodes) != 4 {
		t.Fatalf("Normalized() produced %d nodes, want 4", len(norm.Nodes))
	}

	// Declaration order: postgres, pgbouncer, pgpool, etcd.
	byName := map[string]ProcessNode{}
	for _, n := range norm.Nodes {
		byName[n.Name] = n
	}
	if got := byName["postgres"].DependsOn; len(got) != 0 {
		t.Errorf("postgres DependsOn = %v, want empty", got)
	}
	if got := byName["pgbouncer"].DependsOn; !reflect.DeepEqual(got, []string{"postgres"}) {
		t.Errorf("pgbouncer DependsOn = %v, want [postgres]", got)
	}
	if !byName["pgbouncer"].NeedParentReady {
		t.Error("pgbouncer NeedParentReady lost after normalization")
	}
	if got := byName["pgpool"].DependsOn; !reflect.DeepEqual(got, []string{"pgbouncer"}) {
		t.Errorf("pgpool DependsOn = %v, want [pgbouncer]", got)
	}
	if !byName["pgpool"].NeedParentReady {
		t.Error("pgpool NeedParentReady lost after normalization")
	}
	if got := byName["etcd"].DependsOn; !reflect.DeepEqual(got, []string{"postgres"}) {
		t.Errorf("etcd DependsOn = %v, want [postgres]", got)
	}

	// Flat form must be a valid DAG.
	if err := ValidateChain(norm); err != nil {
		t.Fatalf("normalized chain fails validation: %v", err)
	}

	// Normalizing twice is idempotent.
	again, err := norm.Normalized()
	if err != nil {
		t.Fatalf("second Normalized() error: %v", err)
	}
	if !reflect.DeepEqual(again, norm) {
		t.Error("Normalized() is not idempotent")
	}
}

// TestNormalizedFlatPassthrough verifies an already-flat Nodes chain is returned
// with its dependency fields unchanged, and gains the canonical derived edges
// (dependsOn+needParentReady → a `ready` edge).
func TestNormalizedFlatPassthrough(t *testing.T) {
	chain := ProcessChain{Nodes: []ProcessNode{
		{Name: "a"},
		{Name: "b", DependsOn: []string{"a"}, NeedParentReady: true},
	}}
	norm, err := chain.Normalized()
	if err != nil {
		t.Fatalf("Normalized() error: %v", err)
	}
	// Dependency fields (and all other node fields) are preserved unchanged.
	if got := norm.Nodes[1].DependsOn; !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("DependsOn = %v, want [a]", got)
	}
	if !norm.Nodes[1].NeedParentReady {
		t.Error("NeedParentReady lost after normalization")
	}
	// The canonical edges are derived (ready, from needParentReady).
	if !reflect.DeepEqual(norm.Nodes[1].Edges, []Edge{{Name: "a", WaitFor: WaitReady}}) {
		t.Errorf("Edges = %v, want [a/ready]", norm.Nodes[1].Edges)
	}
	if len(norm.Nodes[0].Edges) != 0 {
		t.Errorf("a Edges = %v, want empty", norm.Nodes[0].Edges)
	}
	// The caller's chain is not mutated.
	if len(chain.Nodes[1].Edges) != 0 {
		t.Error("Normalized() mutated the input chain's Edges")
	}
}

// TestNormalizedEmpty verifies an empty chain normalizes to an empty chain.
func TestNormalizedEmpty(t *testing.T) {
	norm, err := (ProcessChain{}).Normalized()
	if err != nil {
		t.Fatalf("Normalized() error: %v", err)
	}
	if len(norm.Nodes) != 0 {
		t.Fatalf("Normalized() produced %d nodes, want 0", len(norm.Nodes))
	}
}

// TestNormalizedChildrenDependsOnMix verifies a node with both Children and
// DependsOn is rejected at desugar time.
func TestNormalizedChildrenDependsOnMix(t *testing.T) {
	chain := ProcessChain{Roots: []ProcessNode{
		{Name: "a", DependsOn: []string{"z"}, Children: []ProcessNode{{Name: "c"}}},
	}}
	_, err := chain.Normalized()
	if err == nil {
		t.Fatal("Normalized() = nil error, want rejection of Children+DependsOn mix")
	}
	if !strings.Contains(err.Error(), "both Children and DependsOn") {
		t.Fatalf("Normalized() error = %q, want mention of both Children and DependsOn", err)
	}
}

// TestNormalizedChildrenDependsOnEdgesMix verifies a node with both Children
// and DependsOnEdges is rejected at desugar time.
func TestNormalizedChildrenDependsOnEdgesMix(t *testing.T) {
	chain := ProcessChain{Roots: []ProcessNode{
		{Name: "a", DependsOnEdges: []Dependency{{Name: "z", WaitFor: WaitReady}}, Children: []ProcessNode{{Name: "c"}}},
	}}
	_, err := chain.Normalized()
	if err == nil {
		t.Fatal("Normalized() = nil error, want rejection of Children+DependsOnEdges mix")
	}
	if !strings.Contains(err.Error(), "both Children and DependsOnEdges") {
		t.Fatalf("Normalized() error = %q, want mention of both Children and DependsOnEdges", err)
	}
}

// TestNormalizedDependsOnEdgesPassthrough verifies the per-edge dependsOnEdges
// form derives canonical edges directly, preserving each waitFor.
func TestNormalizedDependsOnEdgesPassthrough(t *testing.T) {
	chain := ProcessChain{Nodes: []ProcessNode{
		{Name: "a"},
		{Name: "b", DependsOnEdges: []Dependency{{Name: "a", WaitFor: WaitReady}}},
	}}
	norm, err := chain.Normalized()
	if err != nil {
		t.Fatalf("Normalized() error: %v", err)
	}
	if !reflect.DeepEqual(norm.Nodes[1].Edges, []Edge{{Name: "a", WaitFor: WaitReady}}) {
		t.Errorf("Edges = %v, want [a/ready]", norm.Nodes[1].Edges)
	}
}

// TestNormalizedDependsOnEdgesDefaultStarted verifies an omitted waitFor on a
// dependsOnEdges entry defaults to started.
func TestNormalizedDependsOnEdgesDefaultStarted(t *testing.T) {
	chain := ProcessChain{Nodes: []ProcessNode{
		{Name: "a"},
		{Name: "b", DependsOnEdges: []Dependency{{Name: "a"}}},
	}}
	norm, err := chain.Normalized()
	if err != nil {
		t.Fatalf("Normalized() error: %v", err)
	}
	if !reflect.DeepEqual(norm.Nodes[1].Edges, []Edge{{Name: "a", WaitFor: WaitStarted}}) {
		t.Errorf("Edges = %v, want [a/started]", norm.Nodes[1].Edges)
	}
}

// TestNormalizedDependsOnEdgesUnknownWaitFor verifies an unknown waitFor is
// rejected, naming the node and edge.
func TestNormalizedDependsOnEdgesUnknownWaitFor(t *testing.T) {
	chain := ProcessChain{Nodes: []ProcessNode{
		{Name: "a"},
		{Name: "b", DependsOnEdges: []Dependency{{Name: "a", WaitFor: "redy"}}},
	}}
	_, err := chain.Normalized()
	if err == nil {
		t.Fatal("Normalized() = nil error, want unknown waitFor rejection")
	}
	if !strings.Contains(err.Error(), `node "b"`) || !strings.Contains(err.Error(), `edge "a"`) {
		t.Fatalf("Normalized() error = %q, want it to name node b and edge a", err)
	}
}

// TestNormalizedDependsOnAndEdgesMixRejected verifies DependsOn and
// DependsOnEdges are mutually exclusive at normalize time.
func TestNormalizedDependsOnAndEdgesMixRejected(t *testing.T) {
	chain := ProcessChain{Nodes: []ProcessNode{
		{Name: "a"},
		{Name: "b", DependsOn: []string{"a"}, DependsOnEdges: []Dependency{{Name: "a", WaitFor: WaitReady}}},
	}}
	_, err := chain.Normalized()
	if err == nil {
		t.Fatal("Normalized() = nil error, want DependsOn+DependsOnEdges mix rejection")
	}
	if !strings.Contains(err.Error(), "both dependsOn and dependsOnEdges") {
		t.Fatalf("Normalized() error = %q, want mention of both dependsOn and dependsOnEdges", err)
	}
}

// TestNormalizedTreeWithDependsOnEdges verifies the tree desugar appends the
// parent edge to a child's dependsOnEdges (preserving per-edge wait modes) when
// the child already uses the per-edge form.
func TestNormalizedTreeWithDependsOnEdges(t *testing.T) {
	chain := ProcessChain{Roots: []ProcessNode{
		{Name: "a", Children: []ProcessNode{
			{Name: "b", NeedParentReady: true, DependsOnEdges: []Dependency{{Name: "x", WaitFor: WaitReady}}},
		}},
		{Name: "x"},
	}}
	norm, err := chain.Normalized()
	if err != nil {
		t.Fatalf("Normalized() error: %v", err)
	}
	if err := ValidateChain(norm); err != nil {
		t.Fatalf("normalized chain fails validation: %v", err)
	}
	byName := map[string]ProcessNode{}
	for _, n := range norm.Nodes {
		byName[n.Name] = n
	}
	want := []Edge{{Name: "x", WaitFor: WaitReady}, {Name: "a", WaitFor: WaitReady}}
	if got := byName["b"].Edges; !reflect.DeepEqual(got, want) {
		t.Errorf("b Edges = %v, want %v", got, want)
	}
}
