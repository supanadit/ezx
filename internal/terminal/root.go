// Package terminal is the delivery layer: it owns the CLI surface (cobra
// commands) and delegates all work to Services. It contains no business logic
// and no adapter wiring beyond what is injected.
package terminal

import "github.com/spf13/cobra"

// NewRootCmd returns the root ezx command.
func NewRootCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ezx",
		Short: "Container entrypoint supervisor and bootstrapper",
	}
}
