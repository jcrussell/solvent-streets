package snapshots

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/pkg/cmd/prompt"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

type RmOptions struct {
	IO            *iostreams.IOStreams
	Prompter      prompt.Prompter
	RootDB        func() (db.RootStorer, error)
	ResolveCities func() ([]config.CityConfig, error)
	Config        func() (*config.Config, error)
	SnapshotID    int64
	Yes           bool
}

func NewCmdRm(f *cmdutil.Factory, runF func(context.Context, *RmOptions) error) *cobra.Command {
	opts := &RmOptions{
		IO:            f.IOStreams,
		Prompter:      f.Prompter,
		RootDB:        func() (db.RootStorer, error) { return f.RootDB() },
		ResolveCities: func() ([]config.CityConfig, error) { return cmdutil.ResolveCities(f) },
		Config:        func() (*config.Config, error) { return f.Config() },
	}

	cmd := &cobra.Command{
		Use:   "rm <id>",
		Short: "Delete a snapshot and its FK-linked result rows",
		Long: `Delete a snapshot and all its FK-linked result rows (compute_results,
hex_stats, forecast_results, cohort_stats) in a single transaction.

Snapshot IDs are unique across cities; the command searches every
configured city for the id and deletes from whichever city owns it.
Use --city to restrict the lookup to one city.

Confirmation: prompts on TTY by default. Pass --yes/-y to skip the
prompt for non-interactive use (scripts, CI). Without --yes and
without a TTY the command refuses to delete.`,
		Example: `  # Delete snapshot 42 from whichever city owns it (prompts)
  pvmt snapshots rm 42

  # Skip the confirmation prompt
  pvmt snapshots rm 42 --yes

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

	cmd.Flags().BoolVarP(&opts.Yes, "yes", "y", false, "Skip the interactive confirmation")

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
	configID := cmdutil.ResolveConfigID(opts.Config)

	// Discovery: find the city that owns the snapshot before deleting,
	// so the confirmation prompt can name the owner. Splitting discovery
	// from delete also means a "no" answer leaves the DB untouched.
	var ownerCity *config.CityConfig
	var ownerStore db.Store
	for i := range cities {
		city := &cities[i]
		id, err := root.EnsureCity(ctx, city.Slug(), city.Name, configID)
		if err != nil {
			return fmt.Errorf("ensure city %s: %w", city.Slug(), err)
		}
		store := root.ForCity(id)
		if err := store.ResolveSnapshot(ctx, opts.SnapshotID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return fmt.Errorf("resolve snapshot in %s: %w", city.Slug(), err)
		}
		ownerCity = city
		ownerStore = store
		break
	}
	if ownerCity == nil {
		return cmdutil.Hintf(
			fmt.Errorf("snapshot %d not found", opts.SnapshotID),
			"check 'pvmt snapshots ls' for available ids",
		)
	}

	if err := confirmDestructive(ctx, opts.IO, opts.Prompter, opts.Yes,
		fmt.Sprintf("Delete snapshot %d from %s?", opts.SnapshotID, ownerCity.Slug()),
		fmt.Sprintf("refusing to delete snapshot %d from %s without confirmation",
			opts.SnapshotID, ownerCity.Slug()),
	); err != nil {
		return err
	}

	ok, err := ownerStore.DeleteSnapshot(ctx, opts.SnapshotID)
	if err != nil {
		return fmt.Errorf("delete snapshot in %s: %w", ownerCity.Slug(), err)
	}
	if !ok {
		// Lost a race with another deleter between ResolveSnapshot and
		// DeleteSnapshot — treat as not-found rather than a silent
		// success.
		return cmdutil.Hintf(
			fmt.Errorf("snapshot %d not found", opts.SnapshotID),
			"check 'pvmt snapshots ls' for available ids",
		)
	}
	fmt.Fprintf(opts.IO.ErrOut, "Deleted snapshot %d from %s.\n", opts.SnapshotID, ownerCity.Slug())
	return nil
}
