package snapshots

import (
	"context"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/pkg/cmd/prompt"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

type PruneOptions struct {
	IO            *iostreams.IOStreams
	Prompter      prompt.Prompter
	RootDB        func() (db.RootStorer, error)
	ResolveCities func() ([]config.CityConfig, error)
	Keep          int
	Yes           bool
}

func NewCmdPrune(f *cmdutil.Factory, runF func(context.Context, *PruneOptions) error) *cobra.Command {
	opts := &PruneOptions{
		IO:            f.IOStreams,
		Prompter:      f.Prompter,
		RootDB:        func() (db.RootStorer, error) { return f.RootDB() },
		ResolveCities: func() ([]config.CityConfig, error) { return cmdutil.ResolveCities(f) },
	}

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Retain the N most recent snapshots per city",
		Long: `Delete every snapshot beyond the N most recent per city, cascading to
FK-linked result rows. --keep is required and must be > 0.

Scope: all configured cities by default; restrict to one with --city.

Confirmation: prompts on TTY by default. Pass --yes/-y to skip the
prompt for non-interactive use (scripts, CI). Without --yes and
without a TTY the command refuses to prune.`,
		Example: `  # Keep the 5 most recent snapshots in every city (prompts)
  pvmt snapshots prune --keep=5

  # Skip the confirmation prompt
  pvmt snapshots prune --keep=5 --yes

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
	cmd.Flags().BoolVarP(&opts.Yes, "yes", "y", false, "Skip the interactive confirmation")
	_ = cmd.MarkFlagRequired("keep")

	return cmd
}

// pruneVictims pairs a city with the snapshots that fall outside its
// keep window, captured during the discovery pass so the confirmation
// prompt can quote real totals and so a declined prompt leaves the DB
// untouched.
type pruneVictims struct {
	city    config.CityConfig
	store   db.Store
	victims []db.Snapshot
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

	// Discovery: collect every city's victims first so we can quote the
	// total in the prompt and bail without touching the DB on "no".
	var plan []pruneVictims
	var totalVictims int
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
		plan = append(plan, pruneVictims{city: city, store: store, victims: victims})
		totalVictims += len(victims)
	}

	if totalVictims == 0 {
		fmt.Fprintln(opts.IO.ErrOut, "Nothing to prune.")
		return nil
	}

	if err := confirmDestructive(ctx, opts.IO, opts.Prompter, opts.Yes,
		fmt.Sprintf("Delete %d snapshot(s) across %d city(ies)?", totalVictims, len(plan)),
		fmt.Sprintf("refusing to prune %d snapshot(s) across %d city(ies) without confirmation",
			totalVictims, len(plan)),
	); err != nil {
		return err
	}

	var totalDeleted int
	for _, p := range plan {
		for _, s := range p.victims {
			ok, err := p.store.DeleteSnapshot(ctx, s.ID)
			if err != nil {
				return fmt.Errorf("delete snapshot %d in %s: %w", s.ID, p.city.Slug(), err)
			}
			if ok {
				totalDeleted++
			}
		}
		fmt.Fprintf(opts.IO.ErrOut, "Pruned %d snapshot(s) from %s (kept %d).\n",
			len(p.victims), p.city.Slug(), opts.Keep)
	}
	return nil
}
