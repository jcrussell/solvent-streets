// Package snapshots implements the `pvmt snapshots` command group: create,
// list, delete and prune, for freezing each city's latest compute and
// managing the snapshots table and its FK-linked result rows.
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
		Short: "Create and manage snapshots",
		Long: `Create, list, delete, and prune snapshots. A snapshot is a city's
complete computed state, one run per part, frozen with the hex edge and
boundary it was computed on. create freezes each city's latest compute,
after checking that every part was computed from the inputs stored now.`,
		Example: `  # Snapshot every configured city's latest compute
  pvmt snapshots create

  # List snapshots across every configured city
  pvmt snapshots list

  # Delete one snapshot (and the runs only it used)
  pvmt snapshots delete 42

  # Keep only the 5 most recent snapshots per city
  pvmt snapshots prune --keep=5`,
	}
	cmdutil.MarkGroup(cmd)
	cmd.AddCommand(NewCmdCreate(f, nil))
	cmd.AddCommand(NewCmdList(f, nil))
	cmd.AddCommand(NewCmdDelete(f, nil))
	cmd.AddCommand(NewCmdPrune(f, nil))
	return cmd
}
