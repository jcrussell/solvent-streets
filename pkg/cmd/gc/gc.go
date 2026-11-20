package gc

import (
	"context"
	"errors"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/resource"
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
	// Exporter is set by --json: the report is one row per city instead of
	// text.
	Exporter cmdutil.Exporter

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
		Args:  cobra.NoArgs,
		Short: "Sweep orphaned rows that snapshots prune and delete can't reach",
		Long: `Garbage-collect database rows that 'pvmt snapshots prune' and
'pvmt snapshots delete' structurally cannot reach:

  - Roads, parking and sidewalks rows whose source_api is not a source
    this city resolves from config (e.g. arcgis_url was removed from
    pvmt.toml). Rows with an empty source_api are never swept: they
    cannot be attributed to a source. Overpass rows on a city with
    overpass unset or false are reported and left in place;
    --sweep-disabled-sources deletes them.
  - Runs neither a snapshot nor a latest compute references, with every
    row they wrote: replaced in the latest compute by a later compute, or
    left behind by a compute that crashed or was killed before it
    committed. Runs younger than a day are never swept, because they may
    belong to a compute that is still running.
  - Latest computes made by an older pvmt whose compute logic differs
    (a lower ComputeEpoch). No config can produce their hash again, so
    they are dropped and their runs swept as above, unless a snapshot
    holds them. Another config's latest compute is kept.

Scope: all configured cities by default; restrict to one with --city.

Limitation: a source that is still configured but has been emptied
upstream (the partial-re-ingest residual) is NOT gc-detectable — gc
cannot distinguish stale-but-valid rows from current ones. That case
is cured by a successful full re-ingest, not by gc.

` + cmdutil.ConfirmHelp("delete"),
		Example: `  # Report what would be collected, without deleting
  pvmt gc --dry-run

  # Garbage-collect a single city (prompts)
  pvmt --city oakland-ca gc

  # Skip the confirmation prompt
  pvmt gc --yes

  # Also delete overpass rows for a city that has overpass switched off
  pvmt gc --sweep-disabled-sources

  # Each city's orphan counts as JSON, without deleting
  pvmt gc --dry-run --json city,stale_feature_count,orphan_run_count`,
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
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, gcFields)

	return cmd
}

var gcFields = []string{"city", "stale_feature_count", "superseded_part_count", "orphan_run_count",
	"orphan_city_metric_count", "orphan_hex_metric_count", "kept_feature_count", "deleted"}

// gcRow is one city's --json row: what a scan found or a sweep deleted.
type gcRow struct {
	City   string
	Report db.GCReport
	// Kept counts the overpass rows held back for a city with overpass
	// switched off (retainedOverpassRows); a scan of such a city reports it,
	// a sweep or a city with overpass on not.
	Kept int
	// HoldsBack is whether the scan held overpass rows back at all (overpass
	// off and no --sweep-disabled-sources): kept_feature_count applies.
	HoldsBack bool
	// Deleted is true for a sweep's row, false for a scan's (--dry-run,
	// nothing to collect).
	Deleted bool
}

var _ cmdutil.RowExporter = gcRow{}

func (r gcRow) ExportData(fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		switch f {
		case "city":
			out[f] = r.City
		case "stale_feature_count":
			out[f] = r.Report.StaleFeatures
		case "superseded_part_count":
			out[f] = r.Report.SupersededParts
		case "orphan_run_count":
			out[f] = r.Report.OrphanRuns
		case "orphan_city_metric_count":
			out[f] = r.Report.OrphanRunResults.CityMetrics
		case "orphan_hex_metric_count":
			out[f] = r.Report.OrphanRunResults.HexMetrics
		case "kept_feature_count":
			if r.HoldsBack && !r.Deleted {
				out[f] = r.Kept
			}
		case "deleted":
			out[f] = r.Deleted
		}
	}
	return out
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
	keep := city.PavementSources()
	if !city.Overpass && !sweepDisabled {
		keep = append([]string{resource.SourceOverpass}, keep...)
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
	strict, err := store.GCScan(ctx, keepSourcesFor(city, true), config.ComputeEpoch)
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
	rows := report.OrphanRunResults
	fmt.Fprintf(w.Out, "  stale features:        %d\n", report.StaleFeatures)
	if report.SupersededParts > 0 {
		fmt.Fprintf(w.Out, "  superseded latest:     %d part(s) computed by an older pvmt\n", report.SupersededParts)
	}
	fmt.Fprintf(w.Out, "  orphan runs:           %d (city=%d hex=%d metric rows)\n",
		report.OrphanRuns, rows.CityMetrics, rows.HexMetrics)
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
	fmt.Fprintf(w.Out, "  kept (overpass off):   %d — ingested by an older release, which\n", retained)
	fmt.Fprintln(w.Out, "                         fetched overpass regardless of the flag.")
	fmt.Fprintln(w.Out, "                         Pass --sweep-disabled-sources to delete them.")
}

// scanCities runs the read-only orphan count over every city, prints each
// city's report (unless --json), and returns the per-city rows and the grand
// total the confirmation prompt quotes. It performs ZERO writes, which is
// what lets --dry-run and a declined prompt both stop here with the DB
// untouched.
func scanCities(ctx context.Context, opts *Options, root db.RootStorer,
	cities []config.CityConfig) ([]gcRow, int, error) {
	var total int
	var rows []gcRow
	for _, city := range cities {
		store, err := cmdutil.EnsureCityStore(ctx, root, city, cmdutil.ResolveConfigID(opts.Config, &city))
		if err != nil {
			return nil, 0, err
		}
		report, err := store.GCScan(ctx, keepSourcesFor(city, opts.SweepDisabledSources), config.ComputeEpoch)
		if err != nil {
			return nil, 0, fmt.Errorf("scan %s: %w", city.Slug(), err)
		}
		// A failure here is NOT fatal to the run. retainedOverpassRows only
		// feeds the cosmetic "kept (overpass off)" line; propagating its error
		// let a second, purely informational query fail a city whose real scan
		// had just succeeded. The primary GCScan
		// above stays fatal — that one's count is what the prompt quotes.
		retained, err := retainedOverpassRows(ctx, store, city, opts.SweepDisabledSources, report.StaleFeatures)
		if err != nil {
			cmdutil.Warnf(opts.IO, "%s: could not count retained overpass rows: %v", city.Slug(), err)
			retained = 0
		}
		rows = append(rows, gcRow{City: city.Slug(), Report: *report, Kept: retained,
			HoldsBack: !city.Overpass && !opts.SweepDisabledSources})
		total += report.Total()
		if opts.Exporter != nil {
			continue
		}
		// The header names the city even when it is the only one: the
		// report lines below it do not.
		fmt.Fprintf(opts.IO.Out, "\n=== %s ===\n", city.Slug())
		printReport(opts.IO, report)
		printRetained(opts.IO, retained)
	}
	return rows, total, nil
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
	scanned, total, err := scanCities(ctx, opts, root, cities)
	if err != nil {
		return err
	}

	if opts.Exporter != nil && (total == 0 || opts.DryRun) {
		return cmdutil.WriteRows(opts.IO, opts.Exporter, scanned)
	}
	if total == 0 {
		fmt.Fprintln(opts.IO.Out, "Nothing to collect.")
		return nil
	}

	if opts.DryRun {
		// Dry-run: scanning above performed ZERO writes; stop here.
		fmt.Fprintf(opts.IO.Out, "\nDry run: %d row(s) would be collected across %d city(ies); nothing deleted.\n",
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

	return sweepCities(ctx, opts, root, cities, multi)
}

// sweepCities is the sweep pass: it re-ensures each city store, deletes its
// orphans and reports them.
func sweepCities(ctx context.Context, opts *Options, root db.RootStorer, cities []config.CityConfig, multi bool) error {
	var deletedTotal int
	var swept []gcRow
	// failed ends the sweep with err, first writing the --json rows of the
	// cities already swept: their rows are gone whether or not the rest are.
	failed := func(err error) error {
		if opts.Exporter != nil && len(swept) > 0 {
			if werr := cmdutil.WriteRows(opts.IO, opts.Exporter, swept); werr != nil {
				return errors.Join(err, werr)
			}
		}
		return err
	}
	for _, city := range cities {
		store, err := cmdutil.EnsureCityStore(ctx, root, city, cmdutil.ResolveConfigID(opts.Config, &city))
		if err != nil {
			return failed(err)
		}
		keep := keepSourcesFor(city, opts.SweepDisabledSources)
		report, err := store.GCSweep(ctx, keep, config.ComputeEpoch)
		if err != nil {
			return failed(fmt.Errorf("sweep %s: %w", city.Slug(), err))
		}
		deletedTotal += report.Total()
		if opts.Exporter != nil {
			swept = append(swept, gcRow{City: city.Slug(), Report: *report, Deleted: true})
			continue
		}
		if multi {
			fmt.Fprintf(opts.IO.Out, "\n=== %s ===\n", city.Slug())
		}
		fmt.Fprintf(opts.IO.Out, "Collected %d orphaned row(s) from %s.\n", report.Total(), city.Slug())
		printReport(opts.IO, report)
	}
	if opts.Exporter != nil {
		return cmdutil.WriteRows(opts.IO, opts.Exporter, swept)
	}
	fmt.Fprintf(opts.IO.Out, "\nCollected %d row(s) total.\n", deletedTotal)
	return nil
}
