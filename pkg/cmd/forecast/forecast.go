package forecast

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/export"
	fcpkg "github.com/jcrussell/solvent-streets/internal/forecast"
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
	Exporter    cmdutil.Exporter
	rows        *[]forecastRow // multi-city accumulator; nil for single-pass JSON
}

var forecastFields = []string{"city", "resource_type", "year", "pci", "area", "annual_need", "cost_tier"}

// forecastRow is one simulated baseline year of one resource, the unit of
// the --json output. City is the city slug, so multi-city output stays
// distinguishable.
type forecastRow struct {
	City         string
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
		Short: "Project pavement deterioration and maintenance costs",
		Long: `Project PCI decay and maintenance costs over the [forecast] horizon.

The forecast is simulated each time from the city's current snapshot (its
compute results and cohort stats) under the current [forecast] settings, the
same way export and serve simulate it. Nothing is stored and no snapshot is
committed. A city with nothing computed under its current settings exits 3.`,
		Example: `  # Run baseline + scenario comparisons for every configured city
  pvmt forecast

  # Skip scenario comparisons (baseline only)
  pvmt forecast --scenarios=false

  # Emit machine-readable JSON instead of the formatted table
  pvmt forecast --json city,year,pci,annual_need`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runForecastAllCities(cmd.Context(), f, opts)
		},
	}

	cmd.Flags().BoolVar(&opts.Scenarios, "scenarios", true, "Run scenario comparisons")
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, forecastFields)

	return cmd
}

// simulateResource runs rt's baseline (do-nothing) forecast and returns its
// per-year rows, total deferred cost, and the baseline itself.
func simulateResource(city string, rt resource.Source, cohorts []fcpkg.Cohort, years int, params *fcpkg.Params) ([]forecastRow, float64, fcpkg.ScenarioResult) {
	baseline := fcpkg.Simulate(
		fcpkg.Scenario{Name: "baseline", Label: "Baseline (Do Nothing)", Strategy: fcpkg.StrategyDoNothing},
		cohorts, years, params,
	)
	// Collapse the per-band sub-cohort summaries (from ApplyConditionSpread) back
	// to one row per classification for the cohort-breakdown table.
	baseline.FinalCohorts = fcpkg.AggregateCohortSummariesByClass(baseline.FinalCohorts)

	var rows []forecastRow
	var totalDeferredCost float64
	for _, y := range baseline.Years {
		totalDeferredCost += y.AnnualNeed
		rows = append(rows, forecastRow{
			City:         city,
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

func renderBaselineTable(ios *iostreams.IOStreams, rt resource.Source, area, currentPCI float64,
	baseline fcpkg.ScenarioResult, totalDeferredCost float64, years int, sys units.System) error {
	fmt.Fprintf(ios.Out, "=== %s ===\n", rt.Type())
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
				fmt.Sprintf("$%.0f", y.AnnualNeed),
				y.CostTier,
			)
		}
	}
	if err := tp.Render(); err != nil {
		return err
	}
	fmt.Fprintf(ios.Out, "\n  Total %d-year deferred maintenance: $%.0f\n\n", years, totalDeferredCost)

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

func renderScenarioComparisons(ios *iostreams.IOStreams, baseline fcpkg.ScenarioResult,
	cohorts []fcpkg.Cohort, years int, params *fcpkg.Params) error {
	if len(baseline.Years) == 0 {
		return nil
	}
	year1Need := baseline.Years[0].AnnualNeed
	scenarios := fcpkg.SimulateDefaults(year1Need, cohorts, years, params)

	fmt.Fprintf(ios.Out, "  Funding Levels:\n")
	tp := iostreams.NewTablePrinter(ios)
	tp.AddHeader("Scenario", "End PCI", "Annual Budget", "20yr Backlog")
	for _, sr := range scenarios {
		last := sr.Years[len(sr.Years)-1]
		budgetStr := "unconstrained"
		if sr.Scenario.AnnualBudget > 0 {
			budgetStr = fmt.Sprintf("$%.0f", sr.Scenario.AnnualBudget)
		}
		tp.AddRow(
			sr.Scenario.Label,
			fmt.Sprintf("%.1f", last.PCI),
			budgetStr,
			fmt.Sprintf("$%.0f", last.DeferredBacklog),
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
	return cmdutil.ForEachCityRows(ctx, f, opts.IO, opts.Exporter, func(cf *cmdutil.Factory, rows *[]forecastRow) error {
		cityOpts := *opts
		cityOpts.CurrentCity, cityOpts.CityDB, cityOpts.rows = cf.CurrentCity, cf.CityDB, rows
		return runForecast(ctx, &cityOpts)
	})
}

// runForecast simulates and prints one city's forecast. It only reads: the
// store view (CityDB) resolves the city's snapshot for its current settings,
// and nothing is written back.
func runForecast(ctx context.Context, opts *Options) error {
	ios := opts.IO

	cfg, err := opts.Config()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	city, err := opts.CurrentCity()
	if err != nil {
		return fmt.Errorf("city: %w", err)
	}

	store, err := opts.CityDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	fc := cfg.ResolvedForecast(city)
	sys := opts.UnitSystem()

	// Computed, but not under the current settings: there is nothing to
	// forecast from, and "no compute results" for every resource would hide
	// why. Say it once, the way export does.
	snaps, err := store.ListSnapshots(ctx)
	if err != nil {
		return fmt.Errorf("list snapshots: %w", err)
	}
	if db.StaleFor(snaps, cfg.CityHash(city)) {
		fmt.Fprintf(ios.ErrOut, "Warning: %s\n", cfg.StaleHint(city))
		return cmdutil.ErrNoResults
	}

	fmt.Fprintf(ios.ErrOut, "Running %d-year forecast for %s...\n\n", fc.Years, city.Name)

	rows, err := forecastAllResources(ctx, opts, store, city.Slug(), &fc, sys)
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
// same builders export uses, so the CLI's figures match the site's for the
// all-jurisdiction baseline.
func forecastAllResources(ctx context.Context, opts *Options, store db.Store, citySlug string,
	fc *config.ForecastConfig, sys units.System) ([]forecastRow, error) {
	ios := opts.IO
	years := fc.Years
	costTiers := export.ConvertCostTiers(fc)
	var all []forecastRow
	forecasted := false

	for _, rt := range resource.All {
		t := rt.Type()
		// LatestComputeResult returns raw sql.ErrNoRows when this resource has
		// not been computed for the city yet — warn and skip only in that
		// case. Any other error (locked DB, dropped table) must propagate;
		// folding it into the skip path would silently forecast nothing and
		// exit 0.
		result, err := store.LatestComputeResult(ctx, t)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			fmt.Fprintf(ios.ErrOut, "Warning: no compute results for %s, skipping\n", t)
			continue
		case err != nil:
			return nil, fmt.Errorf("forecast %s: %w", t, err)
		}
		cohorts, err := export.BuildCohortsForResource(ctx, rt, result.TotalArea, store, fc)
		if err != nil {
			return nil, fmt.Errorf("forecast %s: %w", t, err)
		}
		params := fcpkg.NewParamsForResource(string(t), fc.GrowthRate, costTiers, fc.TreatmentCycleYears, fc.ResolvedCostOverhead())
		rows, total, baseline := simulateResource(citySlug, rt, cohorts, years, params)
		all = append(all, rows...)
		forecasted = true

		if opts.Exporter == nil {
			if err := renderBaselineTable(ios, rt, result.TotalArea, fc.InitialPCI, baseline, total, years, sys); err != nil {
				return nil, err
			}
			if opts.Scenarios {
				if err := renderScenarioComparisons(ios, baseline, cohorts, years, params); err != nil {
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
