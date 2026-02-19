package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/process"
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

	chain_sample := domain.ProcessChain{
		Roots: []domain.ProcessNode{
			{
				Name: "hello-world",
				Process: domain.Process{
					BinaryPath: "/bin/echo",
					Arguments:  []string{"Hello, EZX!"},
					WorkingDir: "/tmp",
				},
			},
		},
	}
	s := process.NewService()
	s.Execute(context.TODO(), chain_sample.Roots[0])
	fmt.Println("✅ EZX started successfully!")
}
