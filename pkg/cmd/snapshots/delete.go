package snapshots

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/hint"
	"github.com/jcrussell/solvent-streets/pkg/cmd/prompt"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

type DeleteOptions struct {
	IO            *iostreams.IOStreams
	Prompter      prompt.Prompter
	RootDB        func() (db.RootStorer, error)
	ResolveCities func() ([]config.CityConfig, error)
	Config        func() (*config.Config, error)
	SnapshotID    int64
	Yes           bool
}

func NewCmdDelete(f *cmdutil.Factory, runF func(context.Context, *DeleteOptions) error) *cobra.Command {
	opts := &DeleteOptions{
		IO:            f.IOStreams,
		Prompter:      f.Prompter,
		RootDB:        func() (db.RootStorer, error) { return f.RootDB() },
		ResolveCities: func() ([]config.CityConfig, error) { return cmdutil.ResolveCities(f) },
		Config:        func() (*config.Config, error) { return f.Config() },
	}

	cmd := &cobra.Command{
		Use:     "delete <id>",
		Aliases: []string{"rm"},
		Short:   "Delete a snapshot and the runs only it used",
		Long: `Delete a snapshot, then every run it referenced that no other snapshot
references, with all the result rows those runs wrote, in a single
transaction. Runs another snapshot or the latest compute still uses are
kept, so every remaining snapshot reads exactly what it did before. Export
and serve publish each city's newest snapshot, whatever settings it was
created under, so deleting that one rolls the city back to the one before.

Snapshot IDs are unique across cities; the command searches every
configured city for the id and deletes from whichever city owns it.
Use --city to restrict the lookup to one city.

` + cmdutil.ConfirmHelp("delete"),
		Example: `  # Delete snapshot 42 from whichever city owns it (prompts)
  pvmt snapshots delete 42

  # Skip the confirmation prompt
  pvmt snapshots delete 42 --yes

  # Scope the lookup to one city
  pvmt --city oakland-ca snapshots delete 42`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := db.ParseSnapshotID(args[0])
			if err != nil {
				return cmdutil.FlagErrorf("%v", err)
			}
			opts.SnapshotID = id
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runDelete(cmd.Context(), opts)
		},
	}

	cmd.Flags().BoolVarP(&opts.Yes, "yes", "y", false, "Skip the interactive confirmation")

	return cmd
}

func runDelete(ctx context.Context, opts *DeleteOptions) error {
	cities, err := opts.ResolveCities()
	if err != nil {
		return err
	}
	root, err := opts.RootDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	// Discovery: find the city that owns the snapshot before deleting,
	// so the confirmation prompt can name the owner. Splitting discovery
	// from delete also means a "no" answer leaves the DB untouched.
	var ownerCity *config.CityConfig
	var ownerStore db.Store
	for i := range cities {
		city := &cities[i]
		store, err := cmdutil.EnsureCityStore(ctx, root, *city, cmdutil.ResolveConfigID(opts.Config, city))
		if err != nil {
			return err
		}
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
		return hint.Withf(
			fmt.Errorf("snapshot %d not found", opts.SnapshotID),
			"check 'pvmt snapshots list' for available ids",
		)
	}

	if err := cmdutil.ConfirmDestructive(ctx, opts.IO, opts.Prompter, opts.Yes,
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
		return hint.Withf(
			fmt.Errorf("snapshot %d not found", opts.SnapshotID),
			"check 'pvmt snapshots list' for available ids",
		)
	}
	fmt.Fprintf(opts.IO.ErrOut, "Deleted snapshot %d from %s.\n", opts.SnapshotID, ownerCity.Slug())
	return nil
}
