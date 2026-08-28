package domain

import (
	"strings"
	"testing"
)

func TestValidateChain(t *testing.T) {
	tests := []struct {
		name    string
		chain   ProcessChain
		wantErr string
	}{
		{
			name:  "empty chain is valid",
			chain: ProcessChain{},
		},
		{
			name: "single node no deps",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a"},
			}},
		},
		{
			name: "linear chain",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a"},
				{Name: "b", DependsOn: []string{"a"}},
			}},
		},
		{
			name: "fan-in and shared deps",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a"},
				{Name: "b", DependsOn: []string{"a"}},
				{Name: "c", DependsOn: []string{"a", "b"}},
				{Name: "d", DependsOn: []string{"a", "b"}},
			}},
		},
		{
			name:  "empty name",
			chain: ProcessChain{Nodes: []ProcessNode{{Name: ""}}},
			wantErr: "empty name",
		},
		{
			name: "duplicate names",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a"}, {Name: "a"},
			}},
			wantErr: `duplicate node name "a"`,
		},
		{
			name: "unknown dependency",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a"},
				{Name: "b", DependsOn: []string{"nope"}},
			}},
			wantErr: `node "b" depends on unknown node "nope"`,
		},
		{
			name: "self dependency",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a", DependsOn: []string{"a"}},
			}},
			wantErr: `node "a" depends on itself`,
		},
		{
			name: "direct cycle",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a", DependsOn: []string{"b"}},
				{Name: "b", DependsOn: []string{"a"}},
			}},
			wantErr: "cycle",
		},
		{
			name: "three-node cycle",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a", DependsOn: []string{"c"}},
				{Name: "b", DependsOn: []string{"a"}},
				{Name: "c", DependsOn: []string{"b"}},
			}},
			wantErr: "cycle",
		},
		{
			name: "two exec nodes rejected",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a", Exec: true},
				{Name: "b", Exec: true},
			}},
			wantErr: "at most one exec node",
		},
		{
			name: "exec node with non-oneshot DependsOn rejected",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a"},
				{Name: "b", Exec: true, DependsOn: []string{"a"}},
			}},
			wantErr: `exec node "b" may only depend on oneshot nodes, but "a" is not oneshot`,
		},
		{
			name: "exec node with oneshot DependsOn allowed",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "init", Oneshot: true},
				{Name: "app", Exec: true, DependsOn: []string{"init"}},
			}},
		},
		{
			name: "oneshot + exec rejected",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a", Oneshot: true, Exec: true},
			}},
			wantErr: `oneshot node "a" cannot combine Oneshot with Exec`,
		},
		{
			name: "oneshot + scheduler rejected",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a", Oneshot: true, Scheduler: &SchedulerConfig{}},
			}},
			wantErr: `oneshot node "a" cannot combine Oneshot with Scheduler`,
		},
		{
			name: "oneshot + health rejected",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a", Oneshot: true, Health: &HealthConfig{}},
			}},
			wantErr: `oneshot node "a" cannot combine Oneshot with Health`,
		},
		{
			name: "oneshot + needParentReady rejected",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a", Oneshot: true, NeedParentReady: true},
			}},
			wantErr: `oneshot node "a" cannot set needParentReady`,
		},
		{
			name: "oneshot with restart allowed",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a", Oneshot: true, Restart: &RestartPolicy{Mode: RestartOnFailure, MaxRetries: 2}},
			}},
		},
		{
			name: "node depending on exec rejected",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a", Exec: true},
				{Name: "b", DependsOn: []string{"a"}},
			}},
			wantErr: `node "b" depends on exec node "a"`,
		},
		{
			name: "children + dependsOn mix rejected",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a", DependsOn: []string{"b"}, Children: []ProcessNode{{Name: "c"}}},
				{Name: "b"},
			}},
			wantErr: "sets both Children and DependsOn",
		},
		{
			name: "file log dest without filePath rejected",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a", Process: Process{Log: &LogConfig{Stdout: LogDestFile}}},
			}},
			wantErr: `node "a": log destination "file" requires filePath`,
		},
		{
			name: "file log dest on stderr without filePath rejected",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a", Process: Process{Log: &LogConfig{Stderr: LogDestFile}}},
			}},
			wantErr: `node "a": log destination "file" requires filePath`,
		},
		{
			name: "file log dest with filePath valid",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a", Process: Process{Log: &LogConfig{
					Stdout:   LogDestFile,
					FilePath: "/var/log/app.log",
				}}},
			}},
		},
		{
			name: "non-file log dest ignores empty filePath",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a", Process: Process{Log: &LogConfig{Stdout: LogDestStdout}}},
			}},
		},
		{
			name: "dependsOn + dependsOnEdges mix rejected",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a"},
				{Name: "b", DependsOn: []string{"a"}, DependsOnEdges: []Dependency{{Name: "a", WaitFor: WaitReady}}},
			}},
			wantErr: "sets both dependsOn and dependsOnEdges",
		},
		{
			name: "unknown waitFor rejected",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a"},
				{Name: "b", DependsOnEdges: []Dependency{{Name: "a", WaitFor: "redy"}}},
			}},
			wantErr: `node "b": edge "a" has unknown waitFor "redy"`,
		},
		{
			name: "dependsOnEdges valid with mixed modes",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a"},
				{Name: "b", DependsOnEdges: []Dependency{{Name: "a", WaitFor: WaitReady}}},
				{Name: "c", DependsOnEdges: []Dependency{{Name: "a", WaitFor: WaitStarted}, {Name: "b", WaitFor: WaitExit}}},
			}},
		},
		{
			name: "exit edge on oneshot dep valid",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "init", Oneshot: true},
				{Name: "app", DependsOnEdges: []Dependency{{Name: "init", WaitFor: WaitExit}}},
			}},
		},
		{
			name: "unknown dependency via dependsOnEdges rejected",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a"},
				{Name: "b", DependsOnEdges: []Dependency{{Name: "nope", WaitFor: WaitStarted}}},
			}},
			wantErr: `node "b" depends on unknown node "nope"`,
		},
		{
			name: "children + dependsOnEdges mix rejected",
			chain: ProcessChain{Nodes: []ProcessNode{
				{Name: "a", DependsOnEdges: []Dependency{{Name: "b", WaitFor: WaitReady}}, Children: []ProcessNode{{Name: "c"}}},
				{Name: "b"},
			}},
			wantErr: "sets both Children and DependsOnEdges",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateChain(tt.chain)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateChain() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateChain() = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateChain() error = %q, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateChainCycleNamesCycle(t *testing.T) {
	chain := ProcessChain{Nodes: []ProcessNode{
		{Name: "a", DependsOn: []string{"c"}},
		{Name: "b", DependsOn: []string{"a"}},
		{Name: "c", DependsOn: []string{"b"}},
	}}
	err := ValidateChain(chain)
	if err == nil {
		t.Fatal("expected cycle error")
	}
	for _, n := range []string{"a", "b", "c"} {
		if !strings.Contains(err.Error(), n) {
			t.Fatalf("cycle error %q does not name node %q", err, n)
		}
	}
}
