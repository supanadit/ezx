package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/internal/repository/system"
)

func main() {
	fmt.Println("🚀 Starting EZX....")
	home, err := os.UserHomeDir()
	if err != nil {
		panic("You must have a home directory set for ezx to work")
	}

	ezxHomeDir := filepath.Join(home, ".ezx")
	ezxToolDir := filepath.Join(ezxHomeDir, "tools")
	ezxSandboxDir := filepath.Join(ezxHomeDir, "sandbox")

	viper.Set("EZX_DIR_HOME", ezxHomeDir)
	viper.SetDefault("EZX_DIR_TOOLS", ezxToolDir)
	viper.SetDefault("EZX_DIR_SANDBOX", ezxSandboxDir)

	// Dummy example of ProcessChain
	chain := domain.ProcessChain{
		Roots: []domain.ProcessNode{
			{
				Name: "postgresql",
				Process: domain.Process{
					BinaryPath:  "/usr/bin/postgres",
					Arguments:   []string{"-D", "/var/lib/postgresql/data"},
					Environment: []string{"PGDATA=/var/lib/postgresql/data"},
					WorkingDir:  "/var/lib/postgresql",
				},
				Children: []domain.ProcessNode{
					{
						Name: "pgbouncer",
						Process: domain.Process{
							BinaryPath: "/usr/bin/pgbouncer",
							Arguments:  []string{"/etc/pgbouncer/pgbouncer.ini"},
							WorkingDir: "/etc/pgbouncer",
						},
						NeedParentReady: true,
						Children: []domain.ProcessNode{
							{
								Name: "pgpool",
								Process: domain.Process{
									BinaryPath: "/usr/bin/pgpool",
									Arguments:  []string{"-n"},
									WorkingDir: "/etc/pgpool",
								},
								NeedParentReady: true,
							},
						},
					},
					{
						Name: "etcd",
						Process: domain.Process{
							BinaryPath: "/usr/bin/etcd",
							Arguments:  []string{"--data-dir=/var/lib/etcd"},
							WorkingDir: "/var/lib/etcd",
						},
					},
				},
			},
		},
	}
	_ = chain // Avoid unused variable error

	chainSample := domain.ProcessChain{
		Roots: []domain.ProcessNode{
			{
				Name: "hello-world-parent-1",
				Process: domain.Process{
					BinaryPath:  "/bin/sh",
					Arguments:   []string{"-c", "echo \"$GREETING\""},
					Environment: []string{"GREETING=Hello, EZX!"},
					WorkingDir:  "/tmp",
				},
				Children: []domain.ProcessNode{
					{
						Name: "hello-world-child-2",
						Process: domain.Process{
							BinaryPath: "/bin/sh",
							Arguments:  []string{"-c", "echo \"Hello World from child 2!\""},
							WorkingDir: "/tmp",
						},
						NeedParentReady: false,
					},
					{
						Name: "hello-world-child-1",
						Process: domain.Process{
							BinaryPath: "/bin/sh",
							Arguments:  []string{"-c", "echo \"Hello World from child 1!\""},
							WorkingDir: "/tmp",
						},
						NeedParentReady: true,
					},
				},
			},
		},
	}
	pn1 := system.NewProcessNodeRepository(chainSample.Roots[0])
	pn1.Execute(context.Background())
	fmt.Println("✅ EZX started successfully!")
}
