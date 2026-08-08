package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/envutil"
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
					Arguments:   []string{"-c", "echo \"$GREETING\" && cat /tmp/ezx-pgbackrest.conf && cat /tmp/ezx-postgresql.conf && cat /tmp/ezx-kafka.properties && cat /tmp/ezx-php.ini"},
					Environment: []string{"GREETING=Hello, EZX!", "NODE_ROLE=primary", "POSTGRESQL_CONFIG_SHARED_BUFFERS=128MB", "POSTGRESQL_CONFIG_MAX_CONNECTIONS=100", "KAFKA_CONFIG_LOG_RETENTION_MS=60000", "KAFKA_CONFIG_NUM_PARTITIONS=3", "PHP_MEMORY_LIMIT=4096M"},
					WorkingDir:  "/tmp",
				},
				Files: []domain.FileProvision{
					{
						Path:       "/tmp/ezx-pgbackrest.conf",
						Permission: 0640,
						When:       domain.EnvCondition{Name: "NODE_ROLE", Value: "primary"},
						Operations: []domain.FileOperation{
							{Type: domain.FileOpRemove, Pattern: "^pg2-"},
							{Type: domain.FileOpSetProperty, Pattern: "^backup-standby=", Value: "backup-standby=n"},
						},
					},
					{
						Path: "/tmp/ezx-postgresql.conf",
						Operations: []domain.FileOperation{
							{
								Type:           domain.FileOpSetProperty,
								FromEnvPattern: "^POSTGRESQL_CONFIG_(.+)$",
								NameTransform:  domain.NameTransformLower,
								Pattern:        "^[[:space:]]*#?[[:space:]]*${name}[[:space:]]*=.*",
								Value:          "${name} = ${value}",
								ValueFormat:    domain.ValueFormatAuto,
							},
						},
					},
					// Kafka properties: KAFKA_CONFIG_LOG_RETENTION_MS → log.retention.ms=60000
					{
						Path: "/tmp/ezx-kafka.properties",
						Operations: []domain.FileOperation{
							{
								Type:           domain.FileOpSetProperty,
								FromEnvPattern: "^KAFKA_CONFIG_(.+)$",
								NameTransform:  domain.NameTransformSnakeToDot,
								Pattern:        "^${name}=.*",
								Value:          "${name}=${value}",
							},
						},
					},
					// ProcessFunc callback: php.ini memory_limit upsert with unit validation
					{
						Path: "/tmp/ezx-php.ini",
						ProcessFunc: func(editor domain.FileEditor, environ []string) error {
							limit := envutil.Get(environ, "PHP_MEMORY_LIMIT", "")
							if limit == "" {
								return nil
							}
							return editor.Upsert("^[[:space:]]*memory_limit[[:space:]]*=", "memory_limit = "+limit)
						},
					},
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
	if _, err := pn1.Execute(context.Background()); err != nil {
		fmt.Println("❌ EZX failed:", err)
	}
	fmt.Println("✅ EZX started successfully!")
}
