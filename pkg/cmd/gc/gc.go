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

	// SweepDisabledSources opts in to deleting feature rows whose source is
	// still a legal --source value but is switched off for this city (today:
	// overpass=false). Off by default because those rows are much more often
	// valid data written by an older binary than genuine orphans — see
	// keepSourcesFor.
	SweepDisabledSources bool
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
    Neither are overpass rows on a city with overpass unset or false:
    older releases ingested overpass regardless of that flag, so those
    rows are usually real data rather than orphans. They are reported
    and left in place; --sweep-disabled-sources deletes them.
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
  pvmt gc --yes

  # Also delete overpass rows for a city that has overpass switched off
  pvmt gc --sweep-disabled-sources`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runGC(cmd.Context(), opts)
		},
	}

	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Report what would be deleted without deleting")
	cmd.Flags().BoolVarP(&opts.Yes, "yes", "y", false, "Skip the interactive confirmation")
	cmd.Flags().BoolVar(&opts.SweepDisabledSources, "sweep-disabled-sources", false,
		"Also delete rows from a source that is switched off for the city (e.g. overpass=false)")

	return cmd
}

// keepSourcesFor returns the set of source_api values a city legitimately
// produces given its config. A feature whose source_api is none of the kept set
// (and non-empty) is an orphan: removing arcgis_url makes that city's arcgis
// rows sweepable, which is what gc advertises.
//
// "overpass" is the asymmetric case, and it is asymmetric on purpose.
// ingest.AllSources only started honouring city.Overpass recently; before that
// it appended OverpassSource unconditionally. `overpass` defaults to false and
// validateCityFields accepts a city that sets arcgis_url and omits it entirely,
// so a user who never touched the flag can hold a full set of legitimately
// ingested overpass rows. Mirroring AllSources exactly would classify every one
// of them as an orphan on the first `pvmt gc --yes` after an upgrade and delete
// them — silent data loss, reported only as a smaller paved area on the next
// compute.
//
// So overpass rows are kept by default even when the flag is off, and reported
// separately rather than swept. sweepDisabled (the --sweep-disabled-sources
// flag) is the explicit opt-in for a user who really did flip overpass=false
// and wants the old rows gone.
//
// Config validation forbids overpass=false with no arcgis_url, so the keep set
// is never empty for a valid city.
func keepSourcesFor(city config.CityConfig, sweepDisabled bool) []string {
	var keep []string
	if city.Overpass || !sweepDisabled {
		keep = append(keep, "overpass")
	}
	if city.ArcGISURL != "" {
		keep = append(keep, "arcgis")
	}
	return keep
}

// retainedOverpassRows counts the overpass rows keepSourcesFor is deliberately
// holding back for a city with overpass switched off, so the report can name
// them. It re-scans with the strict (mirror-AllSources) keep set and subtracts
// the stale count already reported under the safe one; the difference is
// exactly the overpass rows.
//
// Returns 0 without touching the DB whenever nothing is being held back — the
// flag is on, or the city enables overpass anyway.
func retainedOverpassRows(ctx context.Context, store db.Store, city config.CityConfig,
	sweepDisabled bool, staleUnderSafeKeep int) (int, error) {
	if sweepDisabled || city.Overpass {
		return 0, nil
	}
	strict, err := store.GCScan(ctx, keepSourcesFor(city, true))
	if err != nil {
		return 0, err
	}
	retained := strict.StaleFeatures - staleUnderSafeKeep
	if retained < 0 {
		// Not reachable: the strict keep set is a subset of the safe one, so it
		// can only find more stale rows. Clamp rather than print a negative.
		return 0, nil
	}
	return retained, nil
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

// printRetained names the overpass rows keepSourcesFor held back, and why. It
// prints nothing when there are none, so the common case stays quiet.
//
// The wording has to carry the whole explanation: someone who sees this line is
// almost always someone who never set `overpass` at all, and the honest reading
// is "your data is fine and gc left it alone", not "you have orphans".
func printRetained(w *iostreams.IOStreams, retained int) {
	if retained <= 0 {
		return
	}
	fmt.Fprintf(w.ErrOut, "  kept (overpass off):   %d — ingested by an older release, which\n", retained)
	fmt.Fprintln(w.ErrOut, "                         fetched overpass regardless of the flag.")
	fmt.Fprintln(w.ErrOut, "                         Pass --sweep-disabled-sources to delete them.")
}

// scanCities runs the read-only orphan count over every city, prints each
// city's report, and returns the grand total the confirmation prompt quotes.
// It performs ZERO writes, which is what lets --dry-run and a declined prompt
// both stop here with the DB untouched.
func scanCities(ctx context.Context, opts *Options, root db.RootStorer,
	cities []config.CityConfig, multi bool) (int, error) {
	var total int
	for _, city := range cities {
		store, err := cmdutil.EnsureCityStore(ctx, root, city, cmdutil.ResolveConfigID(opts.Config, &city))
		if err != nil {
			return 0, err
		}
		report, err := store.GCScan(ctx, keepSourcesFor(city, opts.SweepDisabledSources))
		if err != nil {
			return 0, fmt.Errorf("scan %s: %w", city.Slug(), err)
		}
		// A failure here is NOT fatal to the run. retainedOverpassRows only
		// feeds the cosmetic "kept (overpass off)" line; propagating its error
		// let a second, purely informational query fail a city whose real scan
		// had just succeeded (solvent-streets-q48z.21). The primary GCScan
		// above stays fatal — that one's count is what the prompt quotes.
		retained, err := retainedOverpassRows(ctx, store, city, opts.SweepDisabledSources, report.StaleFeatures)
		if err != nil {
			cmdutil.Warnf(opts.IO, "%s: could not count retained overpass rows: %v", city.Slug(), err)
			retained = 0
		}
		if multi {
			fmt.Fprintf(opts.IO.ErrOut, "\n=== %s ===\n", city.Name)
		}
		printReport(opts.IO, report)
		printRetained(opts.IO, retained)
		total += report.Total()
	}
	return total, nil
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
	multi := len(cities) > 1

	// Scan pass: count orphans per city without writing anything, so the
	// confirmation prompt can quote a real total and a declined prompt
	// leaves the DB untouched.
	total, err := scanCities(ctx, opts, root, cities, multi)
	if err != nil {
		return err
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
		store, err := cmdutil.EnsureCityStore(ctx, root, city, cmdutil.ResolveConfigID(opts.Config, &city))
		if err != nil {
			return err
		}
		keep := keepSourcesFor(city, opts.SweepDisabledSources)
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
