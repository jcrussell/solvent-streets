// Package snapshots implements the `pvmt snapshots` command group:
// ls, rm, and prune subcommands for managing the snapshots table and
// its FK-linked result rows.
package snapshots

import (
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"

	"github.com/spf13/cobra"
)

// NewCmdSnapshots returns the parent `snapshots` command. It only
// aggregates subcommands; it has no runFunc of its own.
func NewCmdSnapshots(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshots",
		Short: "Manage compute snapshots",
		Long:  "List, delete, and prune snapshots accumulated by compute runs.",
		Example: `  # List snapshots across every configured city
  pvmt snapshots ls

  # Delete one snapshot (and its FK-linked compute/hex/forecast/cohort rows)
  pvmt snapshots rm 42

  # Keep only the 5 most recent snapshots per city
  pvmt snapshots prune --keep=5`,
	}
	cmd.AddCommand(NewCmdLs(f, nil))
	cmd.AddCommand(NewCmdRm(f, nil))
	cmd.AddCommand(NewCmdPrune(f, nil))
	return cmd
}
