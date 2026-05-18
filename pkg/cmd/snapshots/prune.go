package snapshots

import (
	"context"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

type PruneOptions struct {
	IO            *iostreams.IOStreams
	RootDB        func() (db.RootStorer, error)
	ResolveCities func() ([]config.CityConfig, error)
	Keep          int
}

func NewCmdPrune(f *cmdutil.Factory, runF func(context.Context, *PruneOptions) error) *cobra.Command {
	opts := &PruneOptions{
		IO:            f.IOStreams,
		RootDB:        func() (db.RootStorer, error) { return f.RootDB() },
		ResolveCities: func() ([]config.CityConfig, error) { return cmdutil.ResolveCities(f) },
	}

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Retain the N most recent snapshots per city",
		Long: `Delete every snapshot beyond the N most recent per city, cascading to
FK-linked result rows. --keep is required and must be > 0.

Scope: all configured cities by default; restrict to one with --city.`,
		Example: `  # Keep the 5 most recent snapshots in every city
  pvmt snapshots prune --keep=5

  # Restrict the prune to one city
  pvmt --city oakland snapshots prune --keep=3`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.Keep <= 0 {
				return cmdutil.FlagErrorf("--keep must be > 0, got %d", opts.Keep)
			}
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runPrune(cmd.Context(), opts)
		},
	}

	cmd.Flags().IntVar(&opts.Keep, "keep", 0, "Number of most recent snapshots to retain per city (required)")
	_ = cmd.MarkFlagRequired("keep")

	return cmd
}

func runPrune(ctx context.Context, opts *PruneOptions) error {
	cities, err := opts.ResolveCities()
	if err != nil {
		return err
	}
	root, err := opts.RootDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	var totalDeleted int
	for _, city := range cities {
		id, err := root.EnsureCity(ctx, city.Slug(), city.Name)
		if err != nil {
			return fmt.Errorf("ensure city %s: %w", city.Slug(), err)
		}
		store := root.ForCity(id)
		// ListSnapshots already returns newest-first; everything past
		// the keep window is eligible for deletion.
		snaps, err := store.ListSnapshots(ctx)
		if err != nil {
			return fmt.Errorf("list snapshots for %s: %w", city.Slug(), err)
		}
		if len(snaps) <= opts.Keep {
			continue
		}
		victims := snaps[opts.Keep:]
		for _, s := range victims {
			ok, err := store.DeleteSnapshot(ctx, s.ID)
			if err != nil {
				return fmt.Errorf("delete snapshot %d in %s: %w", s.ID, city.Slug(), err)
			}
			if ok {
				totalDeleted++
			}
		}
		fmt.Fprintf(opts.IO.ErrOut, "Pruned %d snapshot(s) from %s (kept %d).\n",
			len(victims), city.Slug(), opts.Keep)
	}
	if totalDeleted == 0 {
		fmt.Fprintln(opts.IO.ErrOut, "Nothing to prune.")
	}
	return nil
}
