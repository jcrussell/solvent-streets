package gc

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

type Options struct {
	IO            *iostreams.IOStreams
	Prompter      prompt.Prompter
	RootDB        func() (db.RootStorer, error)
	ResolveCities func() ([]config.CityConfig, error)
	Config        func() (*config.Config, error)
	DryRun        bool
	Yes           bool
}

func NewCmdGC(f *cmdutil.Factory, runF func(context.Context, *Options) error) *cobra.Command {
	opts := &Options{
		IO:            f.IOStreams,
		Prompter:      f.Prompter,
		RootDB:        func() (db.RootStorer, error) { return f.RootDB() },
		ResolveCities: func() ([]config.CityConfig, error) { return cmdutil.ResolveCities(f) },
		Config:        func() (*config.Config, error) { return f.Config() },
	}

	cmd := &cobra.Command{
		Use:   "gc",
		Short: "Sweep orphaned rows that snapshot prune can't reach",
		Long: `Garbage-collect database rows that snapshot prune/rm structurally
cannot reach:

  - Stale feature rows whose source_api is no longer produced by any
    source this city resolves from config (e.g. arcgis_url was removed
    from pvmt.toml). Rows with an empty source_api are never swept —
    they predate source tracking and are not safely attributable.
  - Result rows (compute_results, hex_stats, forecast_results,
    cohort_stats) with snapshot_id IS NULL, which snapshot deletion
    never matches because it deletes WHERE snapshot_id = <id>.
  - Defensive: result rows whose snapshot_id references a snapshot that
    no longer exists (FK orphans).

Scope: all configured cities by default; restrict to one with --city.

Limitation: a source that is still configured but has been emptied
upstream (the partial-re-ingest residual) is NOT gc-detectable — gc
cannot distinguish stale-but-valid rows from current ones. That case
is cured by a successful full re-ingest, not by gc.

Confirmation: prompts on TTY by default. Pass --yes/-y to skip the
prompt for non-interactive use (scripts, CI). Without --yes and
without a TTY the command refuses to delete. --dry-run reports counts
and never writes.`,
		Example: `  # Report what would be collected, without deleting
  pvmt gc --dry-run

  # Garbage-collect a single city (prompts)
  pvmt --city oakland gc

  # Skip the confirmation prompt
  pvmt gc --yes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runGC(cmd.Context(), opts)
		},
	}

	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Report what would be deleted without deleting")
	cmd.Flags().BoolVarP(&opts.Yes, "yes", "y", false, "Skip the interactive confirmation")

	return cmd
}

// keepSourcesFor returns the set of source_api values a city legitimately
// produces given its config. Mirrors ingest.AllSources: "overpass" when the
// city uses Overpass, plus "arcgis" when an ArcGIS URL is configured. A
// feature whose source_api is none of these (and non-empty) is an orphan.
func keepSourcesFor(city config.CityConfig) []string {
	var keep []string
	if city.Overpass {
		keep = append(keep, "overpass")
	}
	if city.ArcGISURL != "" {
		keep = append(keep, "arcgis")
	}
	return keep
}

// printReport writes a per-table summary of a GCReport to w. verb is
// "would delete" for dry-run / scan, "deleted" for a completed sweep.
func printReport(w *iostreams.IOStreams, report *db.GCReport) {
	null := report.NullSnapshotResults
	dang := report.DanglingResults
	fmt.Fprintf(w.ErrOut, "  stale features:        %d\n", report.StaleFeatures)
	fmt.Fprintf(w.ErrOut, "  null-snapshot results: compute=%d hex=%d forecast=%d cohort=%d\n",
		null.ComputeResults, null.HexStats, null.ForecastResults, null.CohortStats)
	fmt.Fprintf(w.ErrOut, "  dangling results:      compute=%d hex=%d forecast=%d cohort=%d\n",
		dang.ComputeResults, dang.HexStats, dang.ForecastResults, dang.CohortStats)
}

func runGC(ctx context.Context, opts *Options) error {
	cities, err := opts.ResolveCities()
	if err != nil {
		return err
	}
	root, err := opts.RootDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	configID := cmdutil.ResolveConfigID(opts.Config)
	multi := len(cities) > 1

	// Scan pass: count orphans per city without writing anything, so the
	// confirmation prompt can quote a real total and a declined prompt
	// leaves the DB untouched.
	var total int
	for _, city := range cities {
		store, err := cmdutil.EnsureCityStore(ctx, root, city, configID)
		if err != nil {
			return err
		}
		keep := keepSourcesFor(city)
		report, err := store.GCScan(ctx, keep)
		if err != nil {
			return fmt.Errorf("scan %s: %w", city.Slug(), err)
		}
		if multi {
			fmt.Fprintf(opts.IO.ErrOut, "\n=== %s ===\n", city.Name)
		}
		printReport(opts.IO, report)
		total += report.Total()
	}

	if total == 0 {
		fmt.Fprintln(opts.IO.ErrOut, "Nothing to collect.")
		return nil
	}

	if opts.DryRun {
		// Dry-run: scanning above performed ZERO writes; stop here.
		fmt.Fprintf(opts.IO.ErrOut, "\nDry run: %d row(s) would be collected across %d city(ies); nothing deleted.\n",
			total, len(cities))
		return nil
	}

	if err := cmdutil.ConfirmDestructive(ctx, opts.IO, opts.Prompter, opts.Yes,
		fmt.Sprintf("Delete %d orphaned row(s) across %d city(ies)?", total, len(cities)),
		fmt.Sprintf("refusing to garbage-collect %d row(s) across %d city(ies) without confirmation",
			total, len(cities)),
	); err != nil {
		return err
	}

	// Sweep pass: re-ensure each city store and delete.
	var deletedTotal int
	for _, city := range cities {
		store, err := cmdutil.EnsureCityStore(ctx, root, city, configID)
		if err != nil {
			return err
		}
		keep := keepSourcesFor(city)
		report, err := store.GCSweep(ctx, keep)
		if err != nil {
			return fmt.Errorf("sweep %s: %w", city.Slug(), err)
		}
		if multi {
			fmt.Fprintf(opts.IO.ErrOut, "\n=== %s ===\n", city.Name)
		}
		fmt.Fprintf(opts.IO.ErrOut, "Collected %d orphaned row(s) from %s.\n", report.Total(), city.Slug())
		printReport(opts.IO, report)
		deletedTotal += report.Total()
	}
	fmt.Fprintf(opts.IO.ErrOut, "\nCollected %d row(s) total.\n", deletedTotal)
	return nil
}
