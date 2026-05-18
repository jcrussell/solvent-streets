package snapshots

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

type RmOptions struct {
	IO            *iostreams.IOStreams
	RootDB        func() (db.RootStorer, error)
	ResolveCities func() ([]config.CityConfig, error)
	SnapshotID    int64
}

func NewCmdRm(f *cmdutil.Factory, runF func(context.Context, *RmOptions) error) *cobra.Command {
	opts := &RmOptions{
		IO:            f.IOStreams,
		RootDB:        func() (db.RootStorer, error) { return f.RootDB() },
		ResolveCities: func() ([]config.CityConfig, error) { return cmdutil.ResolveCities(f) },
	}

	cmd := &cobra.Command{
		Use:   "rm <id>",
		Short: "Delete a snapshot and its FK-linked result rows",
		Long: `Delete a snapshot and all its FK-linked result rows (compute_results,
hex_stats, forecast_results, cohort_stats) in a single transaction.

Snapshot IDs are unique across cities; the command searches every
configured city for the id and deletes from whichever city owns it.
Use --city to restrict the lookup to one city.`,
		Example: `  # Delete snapshot 42 from whichever city owns it
  pvmt snapshots rm 42

  # Scope the lookup to one city
  pvmt --city oakland snapshots rm 42`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return cmdutil.FlagErrorf("invalid snapshot id %q: %v", args[0], err)
			}
			if id <= 0 {
				return cmdutil.FlagErrorf("snapshot id must be positive, got %d", id)
			}
			opts.SnapshotID = id
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runRm(cmd.Context(), opts)
		},
	}

	return cmd
}

func runRm(ctx context.Context, opts *RmOptions) error {
	cities, err := opts.ResolveCities()
	if err != nil {
		return err
	}
	root, err := opts.RootDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	for _, city := range cities {
		id, err := root.EnsureCity(ctx, city.Slug(), city.Name)
		if err != nil {
			return fmt.Errorf("ensure city %s: %w", city.Slug(), err)
		}
		store := root.ForCity(id)
		ok, err := store.DeleteSnapshot(ctx, opts.SnapshotID)
		if err != nil {
			return fmt.Errorf("delete snapshot in %s: %w", city.Slug(), err)
		}
		if ok {
			fmt.Fprintf(opts.IO.ErrOut, "Deleted snapshot %d from %s.\n", opts.SnapshotID, city.Slug())
			return nil
		}
	}
	return cmdutil.Hintf(
		fmt.Errorf("snapshot %d not found", opts.SnapshotID),
		"check 'pvmt snapshots ls' for available ids",
	)
}
