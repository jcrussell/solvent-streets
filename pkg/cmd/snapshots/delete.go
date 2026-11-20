package snapshots

import (
	"context"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/hint"
	"github.com/jcrussell/solvent-streets/pkg/cmd/prompt"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

type DeleteOptions struct {
	IO          *iostreams.IOStreams
	Prompter    prompt.Prompter
	RootDB      func() (db.RootStorer, error)
	CityFlagSet func() bool
	Name        string
	Yes         bool
}

func NewCmdDelete(f *cmdutil.Factory, runF func(context.Context, *DeleteOptions) error) *cobra.Command {
	opts := &DeleteOptions{
		IO:          f.IOStreams,
		Prompter:    f.Prompter,
		RootDB:      func() (db.RootStorer, error) { return f.RootDB() },
		CityFlagSet: cityFlagSet(f),
	}

	cmd := &cobra.Command{
		Use:     "delete <name>",
		Aliases: []string{"rm"},
		Short:   "Delete a snapshot and the runs only it used",
		Long: `Delete a snapshot, every city in it, then every run it referenced that
neither another snapshot nor a latest compute references, with all the
result rows those runs wrote, in a single transaction. Runs another
snapshot or the latest compute still uses are kept, so every remaining
snapshot reads exactly what it did before, and creating the same name again
reuses the latest compute's runs. Export and serve publish the newest
snapshot, so deleting that one rolls every city back to the one before.
--city is not supported: a snapshot covers every configured city.

` + cmdutil.ConfirmHelp("delete"),
		Example: `  # Delete snapshot 2026-11 (prompts)
  pvmt snapshots delete 2026-11

  # Skip the confirmation prompt
  pvmt snapshots delete 2026-11 --yes

  # Replace it with the current latest compute
  pvmt snapshots delete 2026-11 --yes && pvmt snapshots create 2026-11`,
		Args:              snapshotNameArgs(1),
		ValidArgsFunction: cmdutil.SnapshotNameCompletion(f, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := rejectCity(opts.CityFlagSet, "delete"); err != nil {
				return err
			}
			opts.Name = args[0]
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
	root, err := opts.RootDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	// Look it up first, so the prompt can say what goes and a "no" leaves
	// the database untouched.
	snap, err := lookupSnapshot(ctx, root, opts.Name)
	if err != nil {
		return err
	}
	if err := cmdutil.ConfirmDestructive(ctx, opts.IO, opts.Prompter, opts.Yes,
		fmt.Sprintf("Delete snapshot %s (%d %s)?", snap.Name, snap.CityCount, cityNoun(snap.CityCount)),
		fmt.Sprintf("refusing to delete snapshot %s without confirmation", snap.Name),
	); err != nil {
		return err
	}

	ok, err := root.DeleteSnapshot(ctx, opts.Name)
	if err != nil {
		return fmt.Errorf("delete snapshot %s: %w", opts.Name, err)
	}
	if !ok {
		// Lost a race with another deleter between the lookup and the
		// delete: not found rather than a silent success.
		return hint.Withf(fmt.Errorf("snapshot %s not found", opts.Name), "check 'pvmt snapshots list' for available names")
	}
	fmt.Fprintf(opts.IO.ErrOut, "Deleted snapshot %s.\n", opts.Name)
	return nil
}
