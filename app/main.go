package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
	"github.com/supanadit/ezx/domain"
	"github.com/supanadit/ezx/internal/repository/system"
	"github.com/supanadit/ezx/orchestrator"
	"github.com/supanadit/ezx/process"
	"github.com/supanadit/ezx/script"
	scriptmodules "github.com/supanadit/ezx/script/modules"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		panic("You must have a home directory set for ezx to work")
	}

	ezxHomeDir := filepath.Join(home, ".ezx")
	viper.Set("EZX_DIR_HOME", ezxHomeDir)
	viper.SetDefault("EZX_DIR_TOOLS", filepath.Join(ezxHomeDir, "tools"))
	viper.SetDefault("EZX_DIR_SANDBOX", filepath.Join(ezxHomeDir, "sandbox"))

	if len(os.Args) < 2 {
		fmt.Println("usage: ezx bootstrap <script.js>")
		os.Exit(1)
	}
	cmd := os.Args[1]
	switch cmd {
	case "bootstrap":
		if len(os.Args) < 3 {
			fmt.Println("usage: ezx bootstrap <script.js>")
			os.Exit(1)
		}
		bootstrap(os.Args[2])
	default:
		fmt.Printf("unknown command %q\n", cmd)
		fmt.Println("usage: ezx bootstrap <script.js>")
		os.Exit(1)
	}
}

// bootstrap runs a user-supplied JavaScript entrypoint script against the ezx
// host API (require("ezx")). It composes the process adapter, orchestrator,
// logger, and registers the host modules.
func bootstrap(path string) {
	log := system.NewLogger()

	// Compose the supervisor used by the optional ezx.chain helper.
	orch := orchestrator.NewService(
		func(node domain.ProcessNode) process.ProcessRepository {
			return system.NewProcessRepository(node)
		},
		log,
	)
	procFactory := func(node domain.ProcessNode) process.ProcessRepository {
		return system.NewProcessRepository(node)
	}

	// Register the aggregate host module exposed to scripts via require("ezx").
	registry := script.NewRegistry()
	registry.Register("ezx", func() any {
		return scriptmodules.NewEzxModule(log, procFactory, orch)
	})

	engine := system.NewScriptEngine(registry)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fmt.Printf("🚀 EZX bootstrapping %s\n", path)
	if err := engine.RunFile(ctx, path); err != nil {
		fmt.Println("❌ EZX failed:", err)
		os.Exit(1)
	}
	fmt.Println("✅ EZX bootstrap complete")
}
