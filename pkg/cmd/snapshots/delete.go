package snapshots

import (
	"context"
	"fmt"
	"slices"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/hint"
	"github.com/jcrussell/solvent-streets/internal/textutil"
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
	DryRun      bool
	Exporter    cmdutil.Exporter
}

// deletedRow is a snapshot `delete` or `prune` reports in --json: a
// snapshots list row, with whether it was deleted (false on a dry run).
type deletedRow struct {
	snapshotRow
	Deleted bool
}

var _ cmdutil.RowExporter = deletedRow{}

var deletedFields = append(slices.Clone(snapshotFields), "deleted")

func (r deletedRow) ExportData(fields []string) map[string]any {
	out := r.snapshotRow.ExportData(fields)
	if slices.Contains(fields, "deleted") {
		out["deleted"] = r.Deleted
	}
	return out
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

  # Say what would be deleted, without deleting or prompting
  pvmt snapshots delete 2026-11 --dry-run

  # Skip the confirmation prompt
  pvmt snapshots delete 2026-11 --yes

  # Report the deletion as JSON
  pvmt snapshots delete 2026-11 --yes --json name,city_count,deleted

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

	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Report what would be deleted without deleting or prompting")
	cmd.Flags().BoolVarP(&opts.Yes, "yes", "y", false, "Skip the interactive confirmation")
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, deletedFields)

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
	if opts.DryRun {
		if opts.Exporter != nil {
			return cmdutil.WriteRows(opts.IO, opts.Exporter, []deletedRow{{snapshotRow: newSnapshotRow(*snap)}})
		}
		fmt.Fprintf(opts.IO.Out, "Would delete snapshot %q (%d %s) and the runs only it uses.\n", snap.Name, snap.CityCount, textutil.Plural(snap.CityCount, "city", "cities"))
		return nil
	}
	if err := cmdutil.ConfirmDestructive(ctx, opts.IO, opts.Prompter, opts.Yes,
		fmt.Sprintf("Delete snapshot %q (%d %s)?", snap.Name, snap.CityCount, textutil.Plural(snap.CityCount, "city", "cities")),
		fmt.Sprintf("refusing to delete snapshot %q without confirmation", snap.Name),
	); err != nil {
		return err
	}

	ok, err := root.DeleteSnapshot(ctx, opts.Name)
	if err != nil {
		return fmt.Errorf("delete snapshot %q: %w", opts.Name, err)
	}
	if !ok {
		// Lost a race with another deleter between the lookup and the
		// delete: not found rather than a silent success.
		// The name was valid a moment ago, so not a usage error: exit 1.
		return hint.Withf(fmt.Errorf("no snapshot named %q: another process deleted it", opts.Name),
			"check 'pvmt snapshots list' for available names")
	}
	if opts.Exporter != nil {
		return cmdutil.WriteRows(opts.IO, opts.Exporter, []deletedRow{{snapshotRow: newSnapshotRow(*snap), Deleted: true}})
	}
	fmt.Fprintf(opts.IO.Out, "Deleted snapshot %q.\n", opts.Name)
	return nil
}
