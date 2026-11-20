package snapshots

import (
	"context"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/pkg/cmd/prompt"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

type PruneOptions struct {
	IO          *iostreams.IOStreams
	Prompter    prompt.Prompter
	RootDB      func() (db.RootStorer, error)
	CityFlagSet func() bool
	Keep        int
	Yes         bool
	DryRun      bool
}

func NewCmdPrune(f *cmdutil.Factory, runF func(context.Context, *PruneOptions) error) *cobra.Command {
	opts := &PruneOptions{
		IO:          f.IOStreams,
		Prompter:    f.Prompter,
		RootDB:      func() (db.RootStorer, error) { return f.RootDB() },
		CityFlagSet: cityFlagSet(f),
	}

	cmd := &cobra.Command{
		Use:   "prune",
		Args:  cobra.NoArgs,
		Short: "Retain the N most recent snapshots",
		Long: `Delete every snapshot beyond the N most recent, then every run neither a
remaining snapshot nor a latest compute references, with the rows those
runs wrote. The newest snapshot, the one export and serve publish, is
always kept. Each snapshot covers every city, so the N kept are N complete
versions: runs the kept snapshots share with deleted ones stay. --keep is
required and must be > 0. --city is not supported: a snapshot covers every
configured city.

` + cmdutil.ConfirmHelp("prune"),
		Example: `  # Keep the 5 most recent snapshots (prompts)
  pvmt snapshots prune --keep=5

  # List what would be deleted, without deleting or prompting
  pvmt snapshots prune --keep=5 --dry-run

  # Skip the confirmation prompt
  pvmt snapshots prune --keep=5 --yes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := rejectCity(opts.CityFlagSet, "prune"); err != nil {
				return err
			}
			if opts.Keep <= 0 {
				return cmdutil.FlagErrorf("--keep must be > 0, got %d", opts.Keep)
			}
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runPrune(cmd.Context(), opts)
		},
	}

	cmd.Flags().IntVar(&opts.Keep, "keep", 0, "Number of most recent snapshots to retain (required)")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Report what would be deleted without deleting or prompting")
	cmd.Flags().BoolVarP(&opts.Yes, "yes", "y", false, "Skip the interactive confirmation")
	_ = cmd.MarkFlagRequired("keep")

	return cmd
}

func runPrune(ctx context.Context, opts *PruneOptions) error {
	root, err := opts.RootDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	// Newest first: everything past the keep window goes. Discovery comes
	// first, so the prompt can quote the total and a "no" writes nothing.
	snaps, err := root.ListSnapshots(ctx)
	if err != nil {
		return err
	}
	if len(snaps) <= opts.Keep {
		fmt.Fprintln(opts.IO.Out, "Nothing to prune.")
		return nil
	}
	victims := snaps[opts.Keep:]
	if opts.DryRun {
		fmt.Fprintf(opts.IO.Out, "Would delete %s, keeping the %d newest:\n", snapshotCount(len(victims)), opts.Keep)
		for _, s := range victims {
			fmt.Fprintf(opts.IO.Out, "  %s\n", s.Name)
		}
		return nil
	}

	if err := cmdutil.ConfirmDestructive(ctx, opts.IO, opts.Prompter, opts.Yes,
		fmt.Sprintf("Delete %s, keeping the %d newest?", snapshotCount(len(victims)), opts.Keep),
		fmt.Sprintf("refusing to prune %s without confirmation", snapshotCount(len(victims))),
	); err != nil {
		return err
	}

	deleted := 0
	for _, s := range victims {
		ok, err := root.DeleteSnapshot(ctx, s.Name)
		if err != nil {
			return fmt.Errorf("delete snapshot %q: %w", s.Name, err)
		}
		if ok {
			deleted++
		}
	}
	fmt.Fprintf(opts.IO.Out, "Pruned %s (kept %d).\n", snapshotCount(deleted), opts.Keep)
	return nil
}

// snapshotCount is n with the noun "snapshot", pluralized.
func snapshotCount(n int) string {
	if n == 1 {
		return "1 snapshot"
	}
	return fmt.Sprintf("%d snapshots", n)
}
