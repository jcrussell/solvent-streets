// Package snapshots implements the `pvmt snapshots` command group: create,
// list, delete and prune. A snapshot is a named, immutable tag
// over every configured city, like a git tag: each city's latest compute,
// frozen together with the hex edge and boundary it was computed on.
package snapshots

import (
	"context"
	"errors"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/hint"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"

	"github.com/spf13/cobra"
)

// NewCmdSnapshots returns the parent `snapshots` command. It only
// aggregates subcommands; it has no runFunc of its own.
func NewCmdSnapshots(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshots",
		Short: "Create and manage snapshots",
		Long: `Create, list, delete and prune snapshots. A snapshot is a
named, immutable tag over every configured city, like a git tag: each
city's complete computed state, one run per part, frozen with the hex edge
and boundary it was computed on. create freezes every city's latest compute
at once, after checking that every part was computed from the inputs stored
now, or freezes nothing. Export and serve publish the newest snapshot;
--snapshot publishes another.`,
		Example: `  # Snapshot every configured city's latest compute
  pvmt snapshots create v0.5.0

  # List snapshots, newest first
  pvmt snapshots list

  # Replace a snapshot: delete it (and the runs only it used), then create it
  pvmt snapshots delete v0.5.0 --yes && pvmt snapshots create v0.5.0

  # Keep only the 5 most recent snapshots
  pvmt snapshots prune --keep=5`,
	}
	cmdutil.MarkGroup(cmd)
	cmd.AddCommand(NewCmdCreate(f, nil))
	cmd.AddCommand(NewCmdList(f, nil))
	cmd.AddCommand(NewCmdDelete(f, nil))
	cmd.AddCommand(NewCmdPrune(f, nil))
	return cmd
}

// fieldCity is the --json field naming a row's city.
const fieldCity = "city"

// cityFlagSet reports whether --city was given (Factory.CityFlagSet).
func cityFlagSet(f *cmdutil.Factory) func() bool {
	return func() bool { return f.CityFlagSet != nil && f.CityFlagSet() }
}

// rejectCity is the error for --city on a command that acts on whole
// snapshots, which cover every city; nil when it was not given.
func rejectCity(set func() bool, verb string) error {
	if set == nil || !set() {
		return nil
	}
	return cmdutil.FlagErrorf("--city is not supported by `snapshots %s`: a snapshot covers every configured city", verb)
}

// snapshotNameArgs requires n arguments, each a valid snapshot name
// (db.CheckSnapshotName).
func snapshotNameArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(n)(cmd, args); err != nil {
			return err
		}
		for _, a := range args {
			if err := db.CheckSnapshotName(a); err != nil {
				return cmdutil.FlagErrorf("%v", err)
			}
		}
		return nil
	}
}

// lookupSnapshot reads the snapshot named name, with a hint naming `list`
// when there is none.
func lookupSnapshot(ctx context.Context, root db.RootStorer, name string) (*db.Snapshot, error) {
	snap, err := root.Snapshot(ctx, name)
	if errors.Is(err, db.ErrSnapshotNotFound) {
		return nil, hint.Withf(fmt.Errorf("snapshot %s not found", name), "check 'pvmt snapshots list' for available names")
	}
	return snap, err
}
