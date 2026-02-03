package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/spf13/viper"
)

func BuildCurl(zigPath string, sandboxDir string) error {
	curlSource := filepath.Join(sandboxDir, "src", "curl")
	installPrefix := filepath.Join(sandboxDir, "dist", "curl")

	// 1. Prepare Environment
	// Use musl target to ensure the binary is static and has NO system dependencies
	target := "x86_64-linux-musl"
	if runtime.GOARCH == "arm64" {
		target = "aarch64-linux-musl"
	}

	// Define Zig as our C Compiler
	ccArg := fmt.Sprintf("%s cc -target %s", zigPath, target)

	env := os.Environ()
	env = append(env, "CC="+ccArg)
	env = append(env, "LDFLAGS=-static")

	// Helper to execute commands with live output
	runStep := func(stepName string, name string, args ...string) error {
		fmt.Printf("\n--- 🚀 [EZX] %s ---\n", stepName)
		cmd := exec.Command(name, args...)
		cmd.Dir = curlSource
		cmd.Env = env

		// This is the magic for verbosity:
		// Pipe command output directly to the EZX process output
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		return cmd.Run()
	}

	// 2. Configure
	// We disable features that usually require external dynamic libs
	// unless EZX has built them (like OpenSSL or Zlib).
	err := runStep("Configuring Curl", "./configure",
		"--prefix="+installPrefix,
		"--disable-shared",
		"--enable-static",
		"--disable-ldap",
		"--without-libpsl",
		"--without-ssl",
	)
	if err != nil {
		return fmt.Errorf("❌ configure failed: %w", err)
	}

	// 3. Build & Install
	// Use -j for faster multi-threaded builds
	jobs := fmt.Sprintf("-j%d", runtime.NumCPU())
	err = runStep("Compiling and Installing", "make", jobs, "install")
	if err != nil {
		// If 'make' isn't on system, EZX should have built its own 'ezx-make'
		return fmt.Errorf("❌ build failed: %w", err)
	}

	fmt.Printf("\n✨ Successfully built Curl in isolated mode at: %s\n", installPrefix)
	return nil
}

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
	BuildCurl("/home/supanadit/SDK/Zig/0.15.2/zig", ezxSandboxDir)
}
