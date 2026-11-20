package forecast

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/export"
	fcpkg "github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/hint"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Options struct {
	IO          *iostreams.IOStreams
	CityDB      func() (db.Store, error)
	Config      func() (*config.Config, error)
	CurrentCity func() (*config.CityConfig, error)
	UnitSystem  func() units.System
	Scenarios   bool
	// Scope is "city" (the default: city-maintained pavement when the city
	// has any, as the dashboard's headline) or "all" (every jurisdiction).
	Scope    string
	Exporter cmdutil.Exporter
	// Snapshot names the snapshot to forecast from; "" forecasts from the
	// latest compute under the current settings.
	Snapshot string
	rows     *[]forecastRow // multi-city accumulator; nil for single-pass JSON
}

// forecastScopes are the --scope values.
var forecastScopes = []string{export.ForecastScopeCity, export.ForecastScopeAll}

var forecastFields = []string{"city", "scope", "resource_type", "year", "pci", "area", "annual_need", "cost_tier"}

// forecastRow is one simulated baseline year of one resource, the unit of
// the --json output. City is the city slug, so multi-city output stays
// distinguishable. Scope is the pavement the forecast covers: "city"
// (city-maintained) or "all" (every jurisdiction).
type forecastRow struct {
	City         string
	Scope        string
	ResourceType resource.Type
	Year         int
	PCI          float64
	Area         float64
	AnnualNeed   float64
	CostTier     string
}

var _ cmdutil.RowExporter = forecastRow{}

func (r forecastRow) ExportData(fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		switch f {
		case "city":
			out[f] = r.City
		case "scope":
			out[f] = r.Scope
		case "resource_type":
			out[f] = string(r.ResourceType)
		case "year":
			out[f] = r.Year
		case "pci":
			out[f] = r.PCI
		case "area":
			out[f] = r.Area
		case "annual_need":
			out[f] = r.AnnualNeed
		case "cost_tier":
			out[f] = r.CostTier
		}
	}
	return out
}

func NewCmdForecast(f *cmdutil.Factory, runF func(context.Context, *Options) error) *cobra.Command {
	opts := &Options{
		IO:          f.IOStreams,
		CityDB:      f.CityDB,
		Config:      f.Config,
		CurrentCity: f.CurrentCity,
		UnitSystem:  f.UnitSystem,
	}

	cmd := &cobra.Command{
		Use:   "forecast",
		Args:  cobra.NoArgs,
		Short: "Project pavement deterioration and maintenance costs",
		Long: `Project PCI decay and maintenance costs over the [forecast] horizon.

The forecast is simulated each time from the city's latest compute (its
compute results and cohort stats), or from the snapshot --snapshot names,
the one export and serve would publish, under the current [forecast]
settings.
By default it covers the pavement the city maintains, as the dashboard's
headline does, when the city has any city-maintained pavement computed, and
every jurisdiction's otherwise. --scope all forecasts every jurisdiction's
pavement, the dashboard's "All" view. Nothing is stored and no snapshot is
committed, and [forecast] settings are not part of the config hash, so a
forecast edit takes effect here without a recompute. A city with nothing
computed under its current settings exits 3.`,
		Example: `  # Run baseline + scenario comparisons for every configured city
  pvmt forecast

  # Skip scenario comparisons (baseline only)
  pvmt forecast --scenarios=false

  # Every jurisdiction's pavement, not only what the city maintains
  pvmt forecast --scope all

  # Forecast from a published snapshot rather than the latest compute
  pvmt forecast --snapshot 2026-11

  # Emit machine-readable JSON instead of the formatted table
  pvmt forecast --json city,year,pci,annual_need`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.Scope != export.ForecastScopeCity && opts.Scope != export.ForecastScopeAll {
				return cmdutil.UnknownValueErrorf("scope", opts.Scope, forecastScopes)
			}
			if opts.Snapshot != "" {
				if err := db.CheckSnapshotName(opts.Snapshot); err != nil {
					return cmdutil.FlagErrorf("--snapshot: %v", err)
				}
			}
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runForecastAllCities(cmd.Context(), f, opts)
		},
	}

	cmd.Flags().BoolVar(&opts.Scenarios, "scenarios", true, "Run scenario comparisons")
	cmd.Flags().StringVar(&opts.Scope, "scope", export.ForecastScopeCity, "Pavement to forecast: city (city-maintained, when the city has any) or all (every jurisdiction)")
	_ = cmd.RegisterFlagCompletionFunc("scope", cobra.FixedCompletions(forecastScopes, cobra.ShellCompDirectiveNoFileComp))
	cmd.Flags().StringVar(&opts.Snapshot, "snapshot", "", "Forecast from the snapshot with this name instead of each city's latest compute (pvmt snapshots list)")
	_ = cmd.RegisterFlagCompletionFunc("snapshot", cmdutil.SnapshotNameCompletion(f, -1))
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, forecastFields)

	return cmd
}

// simulateResource runs rt's baseline (do-nothing) forecast and returns its
// per-year rows, total deferred cost, and the baseline itself.
func simulateResource(city, scope string, rt resource.Source, cohorts []fcpkg.Cohort, years int, params *fcpkg.Params) ([]forecastRow, float64, fcpkg.ScenarioResult) {
	baseline := fcpkg.SimulateBaseline(cohorts, years, params)
	// Collapse the per-band sub-cohort summaries back to one row per
	// classification for the cohort-breakdown table.
	baseline = fcpkg.CollapseBands(baseline)

	var rows []forecastRow
	var totalDeferredCost float64
	for _, y := range baseline.Years {
		totalDeferredCost += y.AnnualNeed
		rows = append(rows, forecastRow{
			City:         city,
			Scope:        scope,
			ResourceType: rt.Type(),
			Year:         y.Year,
			PCI:          y.PCI,
			Area:         y.Area,
			AnnualNeed:   y.AnnualNeed,
			CostTier:     y.CostTier,
		})
	}
	return rows, totalDeferredCost, baseline
}

func renderBaselineTable(ios *iostreams.IOStreams, rt resource.Source, scope string, area, currentPCI float64,
	baseline fcpkg.ScenarioResult, totalDeferredCost float64, years int, sys units.System) error {
	fmt.Fprintf(ios.Out, "=== %s (%s) ===\n", rt.Type(), scopeLabel(scope))
	fmt.Fprintf(ios.Out, "  Current area: %s (%s)\n", units.FormatArea(area, sys), units.FormatAreaLarge(area, sys))
	fmt.Fprintf(ios.Out, "  Initial PCI: %.0f\n\n", currentPCI)

	tp := iostreams.NewTablePrinter(ios)
	tp.AddHeader("Year", "PCI", units.AreaLargeLabel(sys), "Annual Need", "Cost Tier")
	for _, y := range baseline.Years {
		if y.Year <= 5 || y.Year%5 == 0 || y.Year == years {
			tp.AddRow(
				strconv.Itoa(y.Year),
				fmt.Sprintf("%.1f", y.PCI),
				fmt.Sprintf("%.1f", units.AreaLargeValue(y.Area, sys)),
				units.FormatDollars(y.AnnualNeed),
				y.CostTier,
			)
		}
	}
	if err := tp.Render(); err != nil {
		return err
	}
	fmt.Fprintf(ios.Out, "\n  Total %d-year deferred maintenance: %s\n\n", years, units.FormatDollars(totalDeferredCost))

	if len(baseline.FinalCohorts) > 1 {
		fmt.Fprintf(ios.Out, "  Cohort Breakdown:\n")
		cp := iostreams.NewTablePrinter(ios)
		cp.AddHeader("Classification", "Area %", "Decay Rate", "End PCI")
		// Self-normalize over the cohort areas rather than dividing by the
		// year-0 network area: CohortSummary.Area is the final-year *grown*
		// area, so mixing bases would make the column sum to the growth factor
		// (120% at 20yr / growth_rate=0.01) instead of 100%, contradicting the
		// "Current area" line printed above. Mirrors renderCohortBreakdown in
		// internal/export/templates/app.js.
		var cohortArea float64
		for _, c := range baseline.FinalCohorts {
			cohortArea += c.Area
		}
		for _, c := range baseline.FinalCohorts {
			areaPct := 0.0
			if cohortArea > 0 {
				areaPct = c.Area / cohortArea * 100
			}
			cp.AddRow(
				c.Classification,
				fmt.Sprintf("%.1f%%", areaPct),
				fmt.Sprintf("%.3f", c.DecayRate),
				fmt.Sprintf("%.1f", c.EndPCI),
			)
		}
		if err := cp.Render(); err != nil {
			return err
		}
		fmt.Fprintln(ios.Out)
	}
	return nil
}

// scopeLabel names a forecast scope in the table headings.
func scopeLabel(scope string) string {
	if scope == export.ForecastScopeCity {
		return "city-maintained"
	}
	return "all jurisdictions"
}

func renderScenarioComparisons(ios *iostreams.IOStreams, baseline fcpkg.ScenarioResult,
	cohorts []fcpkg.Cohort, years int, params *fcpkg.Params) error {
	if len(baseline.Years) == 0 {
		return nil
	}
	year1Need := baseline.Years[0].AnnualNeed
	scenarios := fcpkg.SimulateDefaults(year1Need, cohorts, years, params)

	fmt.Fprintf(ios.Out, "  Funding Levels:\n")
	tp := iostreams.NewTablePrinter(ios)
	tp.AddHeader("Scenario", "End PCI", "Annual Budget", fmt.Sprintf("%dyr Backlog", years))
	for _, sr := range scenarios {
		last := sr.Years[len(sr.Years)-1]
		budgetStr := "unconstrained"
		if sr.Scenario.AnnualBudget > 0 {
			budgetStr = units.FormatDollars(sr.Scenario.AnnualBudget)
		}
		tp.AddRow(
			sr.Scenario.Label,
			fmt.Sprintf("%.1f", last.PCI),
			budgetStr,
			units.FormatDollars(last.DeferredBacklog),
		)
	}
	if err := tp.Render(); err != nil {
		return err
	}
	fmt.Fprintln(ios.Out)
	return nil
}

// runForecastAllCities fans the forecast out across every configured city
// (or the single --city target); ForEachCityRows writes all cities' JSON rows
// as one document.
func runForecastAllCities(ctx context.Context, f *cmdutil.Factory, opts *Options) error {
	if err := checkSnapshotExists(ctx, f, opts.Snapshot); err != nil {
		return err
	}
	return cmdutil.ForEachCityRows(ctx, f, opts.IO, opts.Exporter, func(cf *cmdutil.Factory, rows *[]forecastRow) error {
		cityOpts := *opts
		cityOpts.CurrentCity, cityOpts.CityDB, cityOpts.rows = cf.CurrentCity, cf.CityDB, rows
		return runForecast(ctx, &cityOpts)
	})
}

// checkSnapshotExists refuses a --snapshot name no snapshot has, once,
// before any city runs; "" (no --snapshot) passes.
func checkSnapshotExists(ctx context.Context, f *cmdutil.Factory, name string) error {
	if name == "" || f.RootDB == nil {
		return nil
	}
	root, err := f.RootDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	if _, err := root.Snapshot(ctx, name); errors.Is(err, db.ErrSnapshotNotFound) {
		return hint.Withf(fmt.Errorf("no snapshot named %q", name), "check 'pvmt snapshots list' for available names")
	} else if err != nil {
		return err
	}
	return nil
}

// runForecast simulates and prints one city's forecast. It only reads: the
// store view (CityDB) resolves the city's snapshot for its current settings,
// and nothing is written back.
func runForecast(ctx context.Context, opts *Options) error {
	ios := opts.IO

	cfg, err := opts.Config()
	if err != nil {
		return err
	}

	city, err := opts.CurrentCity()
	if err != nil {
		return err
	}

	store, err := opts.CityDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	fc := cfg.ResolvedForecast(city)
	sys := opts.UnitSystem()

	if opts.Snapshot != "" {
		id, err := store.ResolveSnapshot(ctx, opts.Snapshot)
		if errors.Is(err, db.ErrSnapshotNotFound) {
			return hint.Withf(fmt.Errorf("snapshot %q does not cover %s", opts.Snapshot, city.Name),
				"check 'pvmt snapshots show %s' for the cities it covers, or pick one with --city", opts.Snapshot)
		}
		if err != nil {
			return fmt.Errorf("resolve snapshot %q for %s: %w", opts.Snapshot, city.Name, err)
		}
		return forecastStore(ctx, opts, store.WithSnapshot(id), city, &fc, sys)
	}

	// Computed, but not under the current settings: the latest compute it
	// forecasts from is empty, and "no compute results" for every resource
	// would hide why. Say it once.
	snaps, err := store.ListSnapshots(ctx)
	if err != nil {
		return fmt.Errorf("list snapshots: %w", err)
	}
	latest, err := store.LatestCompute(ctx, cfg.CityHash(city))
	if err != nil {
		return fmt.Errorf("latest compute: %w", err)
	}
	if export.StaleSettings(cfg, city, snaps, latest) {
		cmdutil.Warnf(ios, "%s", cfg.StaleHint(city))
		return cmdutil.ErrNoResults
	}

	return forecastStore(ctx, opts, store, city, &fc, sys)
}

// forecastStore simulates city from the compute results store reads and
// prints or collects the rows.
func forecastStore(ctx context.Context, opts *Options, store db.Store, city *config.CityConfig, fc *config.ForecastConfig, sys units.System) error {
	ios := opts.IO
	if opts.Exporter == nil {
		fmt.Fprintf(ios.ErrOut, "Running %d-year forecast for %s...\n\n", fc.Years, city.Name)
	}
	rows, err := forecastAllResources(ctx, opts, store, city.Slug(), fc, sys)
	if err != nil {
		return err
	}
	if opts.Exporter == nil {
		return nil
	}
	// Multi-city: append to the shared accumulator, written once by the
	// caller. Single-pass (direct runForecast, no accumulator): write here.
	if opts.rows != nil {
		*opts.rows = append(*opts.rows, rows...)
		return nil
	}
	return cmdutil.WriteRows(ios, opts.Exporter, rows)
}

// forecastAllResources simulates every computed resource from store and
// renders the tables (unless --json). Cohorts and cost tiers come from the
// same builders export uses (export.BuildScopedCohorts), so the CLI's figures
// match the dashboard's for the same scope.
func forecastAllResources(ctx context.Context, opts *Options, store db.Store, citySlug string,
	fc *config.ForecastConfig, sys units.System) ([]forecastRow, error) {
	ios := opts.IO
	years := fc.Years
	costTiers := export.ConvertCostTiers(fc)
	var all []forecastRow
	forecasted := false

	// A real DB failure (locked DB, dropped table) propagates; only a
	// resource with no compute result is skipped below.
	scoped, scope, err := export.BuildScopedCohorts(ctx, store, fc, opts.Scope == export.ForecastScopeAll)
	if err != nil {
		return nil, fmt.Errorf("forecast: %w", err)
	}
	if opts.Scope == export.ForecastScopeCity && scope == export.ForecastScopeAll {
		fmt.Fprintf(ios.ErrOut, "No city-maintained pavement is computed for this city, so the forecast covers every jurisdiction.\n\n")
	}

	for _, rt := range resource.All {
		t := rt.Type()
		sc := scoped[t]
		if !sc.Computed {
			cmdutil.Warnf(ios, "no compute results for %s, skipping", t)
			continue
		}
		params := fcpkg.NewParamsForResource(string(t), fc.GrowthRate, costTiers, fc.TreatmentCycleYears, fc.ResolvedCostOverhead())
		rows, total, baseline := simulateResource(citySlug, scope, rt, sc.Cohorts, years, params)
		all = append(all, rows...)
		forecasted = true

		if opts.Exporter == nil {
			if err := renderBaselineTable(ios, rt, scope, sc.Area, fc.InitialPCI, baseline, total, years, sys); err != nil {
				return nil, err
			}
			if opts.Scenarios {
				if err := renderScenarioComparisons(ios, baseline, sc.Cohorts, years, params); err != nil {
					return nil, err
				}
			}
		}
	}

	// Every resource was skipped (none computed) — signal exit 3 rather than
	// succeeding with an empty forecast.
	if !forecasted {
		return nil, cmdutil.ErrNoResults
	}
	return all, nil
}
