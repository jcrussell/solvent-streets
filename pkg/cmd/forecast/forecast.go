package forecast

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
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

var forecastFields = []string{"resourceType", "year", "pci", "area", "treatmentCost", "treatmentTier"}

// forecastRow wraps db.ForecastResult to attach the CLI JSON export
// contract without pulling cmdutil into the db package.
type forecastRow struct {
	db.ForecastResult
}

var _ cmdutil.RowExporter = forecastRow{}

func (r forecastRow) ExportData(fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		switch f {
		case "resourceType":
			out[f] = r.ResourceType
		case "year":
			out[f] = r.Year
		case "pci":
			out[f] = r.PCI
		case "area":
			out[f] = r.Area
		case "treatmentCost":
			out[f] = r.TreatmentCost
		case "treatmentTier":
			out[f] = r.TreatmentTier
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
		Long:  "Run PCI decay and cost projections over a configurable time horizon.\nShows projected deterioration and deferred maintenance costs.",
		Example: `  # Run baseline + scenario comparisons for every configured city
  pvmt forecast

  # Skip scenario comparisons (baseline only)
  pvmt forecast --scenarios=false

  # Emit machine-readable JSON instead of the formatted table
  pvmt forecast --json year,pci,treatmentCost`,
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

// simulateResource runs the baseline forecast for a single resource type, returning
// the collected results, total deferred cost, and scenario result.
func simulateResource(rt resource.Source, cohorts []fcpkg.Cohort, years int, params *fcpkg.Params, runID int64) ([]db.ForecastResult, float64, fcpkg.ScenarioResult) {
	baseline := fcpkg.Simulate(
		fcpkg.Scenario{Name: "baseline", Label: "Baseline (Do Nothing)", Strategy: fcpkg.StrategyDoNothing},
		cohorts, years, params,
	)
	// Collapse the per-band sub-cohort summaries (from ApplyConditionSpread) back
	// to one row per classification for the cohort-breakdown table.
	baseline.FinalCohorts = fcpkg.AggregateCohortSummariesByClass(baseline.FinalCohorts)

	var results []db.ForecastResult
	var totalDeferredCost float64

	rtVal := rt.Type()
	for _, y := range baseline.Years {
		totalDeferredCost += y.AnnualNeed
		results = append(results, db.ForecastResult{
			ResourceType:  rtVal,
			Year:          y.Year,
			PCI:           y.PCI,
			Area:          y.Area,
			TreatmentCost: y.AnnualNeed,
			TreatmentTier: y.CostTier,
			RunID:         runID,
		})
	}

	return results, totalDeferredCost, baseline
}

func buildForecastCohorts(ctx context.Context, rt resource.Source, area float64, store db.Store, fc *config.ForecastConfig) ([]fcpkg.Cohort, error) {
	currentPCI := fc.InitialPCI
	t := rt.Type()
	stats, err := store.ListCohortStats(ctx, t)
	if err != nil {
		return nil, fmt.Errorf("list cohort stats for %s: %w", t, err)
	}
	var inputs []fcpkg.CohortInput
	for _, st := range stats {
		inputs = append(inputs, fcpkg.CohortInput{
			Classification: st.Classification,
			Area:           st.Area,
		})
	}
	cohorts := fcpkg.BuildCohorts(inputs, currentPCI, fc.DecayRate)
	if cohorts == nil {
		tName := string(t)
		defaultRate := fcpkg.DecayRateForClass(tName)
		if fc.DecayRate > 0 && fcpkg.IsRoadClass(tName) {
			defaultRate = fc.DecayRate
		}
		cohorts = []fcpkg.Cohort{{
			Classification: tName,
			Area:           area,
			DecayRate:      defaultRate,
			InitialPCI:     currentPCI,
		}}
	}
	// Spread the mean PCI into a condition distribution so the CLI matches the
	// exported site (docs/validation.md §4); both share fcpkg.ApplyConditionSpread.
	return fcpkg.ApplyConditionSpread(cohorts), nil
}

func renderBaselineTable(ios *iostreams.IOStreams, rt resource.Source, area, currentPCI float64,
	baseline fcpkg.ScenarioResult, totalDeferredCost float64, years int, sys units.System) error {
	fmt.Fprintf(ios.Out, "=== %s ===\n", rt.Type())
	fmt.Fprintf(ios.Out, "  Current area: %s (%s)\n", units.FormatArea(area, sys), units.FormatAreaLarge(area, sys))
	fmt.Fprintf(ios.Out, "  Initial PCI: %.0f\n\n", currentPCI)

	tp := iostreams.NewTablePrinter(ios)
	tp.AddHeader("Year", "PCI", units.AreaLargeLabel(sys), "Treatment Cost", "Tier")
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

	fmt.Fprintf(ios.ErrOut, "Running %d-year forecast for %s...\n\n", fc.Years, city.Name)

	// One snapshot for the whole command: the newest snapshot for this
	// config with only the forecast parts swapped.
	draft := db.NewDraft(store, cfg.CityHash(city))
	allResults, err := forecastAllResources(ctx, opts, draft, &fc, sys)
	if err != nil {
		return errors.Join(err, draft.Discard(context.WithoutCancel(ctx)))
	}
	if _, err := draft.Commit(ctx); err != nil {
		return errors.Join(fmt.Errorf("commit snapshot: %w", err), draft.Discard(context.WithoutCancel(ctx)))
	}

	if opts.Exporter == nil {
		return nil
	}
	rows := make([]forecastRow, len(allResults))
	for i, r := range allResults {
		rows[i] = forecastRow{r}
	}
	// Multi-city: append to the shared accumulator, written once by the
	// caller. Single-pass (direct runForecast, no accumulator): write here.
	if opts.rows != nil {
		*opts.rows = append(*opts.rows, rows...)
		return nil
	}
	return cmdutil.WriteRows(ios, opts.Exporter, rows)
}

func convertCostTiers(fc *config.ForecastConfig) []fcpkg.CostTier {
	var tiers []fcpkg.CostTier
	for _, t := range fc.CostTiers {
		tiers = append(tiers, fcpkg.CostTier{
			MinPCI:     t.MinPCI,
			MaxPCI:     t.MaxPCI,
			CostPerSqM: t.CostPerSqM,
			Label:      t.Label,
		})
	}
	return tiers
}

// Producer is the snapshot part a resource's forecast fills. A recompute of
// the resource reruns it in the same command (pkg/cmd/compute), so a snapshot
// never pairs a resource with a forecast of an older compute.
func Producer(t resource.Type) string { return "forecast:" + string(t) }

// ResourceForecast is one resource's simulated baseline, as stored and as the
// forecast command renders it.
type ResourceForecast struct {
	Area          float64
	Cohorts       []fcpkg.Cohort
	Params        *fcpkg.Params
	Results       []db.ForecastResult
	TotalDeferred float64
	Baseline      fcpkg.ScenarioResult
}

// ForecastResource simulates rt's baseline from the compute result and cohort
// stats the draft reads (so it sees a recompute made earlier in the same
// command), stores the rows as a new forecast run, and puts that run into the
// draft. It returns raw sql.ErrNoRows when the draft has no compute result for
// rt, and stores nothing in that case.
func ForecastResource(ctx context.Context, draft *db.Draft, rt resource.Source, fc *config.ForecastConfig) (*ResourceForecast, error) {
	t := rt.Type()
	store := draft.Store()
	result, err := store.LatestComputeResult(ctx, t)
	if err != nil {
		return nil, err
	}
	params := fcpkg.NewParamsForResource(string(t), fc.GrowthRate, convertCostTiers(fc), fc.TreatmentCycleYears, fc.ResolvedCostOverhead())
	cohorts, err := buildForecastCohorts(ctx, rt, result.TotalArea, store, fc)
	if err != nil {
		return nil, err
	}
	runID, err := draft.NewRun(ctx, Producer(t))
	if err != nil {
		return nil, err
	}
	rows, total, baseline := simulateResource(rt, cohorts, fc.Years, params, runID)
	if err := store.SaveForecastResults(ctx, rows); err != nil {
		return nil, fmt.Errorf("saving forecast results for %s: %w", t, err)
	}
	draft.Put(Producer(t), runID)
	return &ResourceForecast{Area: result.TotalArea, Cohorts: cohorts, Params: params, Results: rows, TotalDeferred: total, Baseline: baseline}, nil
}

func forecastAllResources(ctx context.Context, opts *Options, draft *db.Draft,
	fc *config.ForecastConfig, sys units.System) ([]db.ForecastResult, error) {
	ios := opts.IO
	years := fc.Years
	var allResults []db.ForecastResult
	forecasted := false

	for _, rt := range resource.All {
		t := rt.Type()
		// ForecastResource returns raw sql.ErrNoRows (never a nil result
		// without an error) when this resource has not been computed for the
		// city yet — warn and skip only in that case. Any other error (locked
		// DB, dropped table) must propagate; folding it into the skip path
		// would silently forecast nothing and exit 0.
		rf, err := ForecastResource(ctx, draft, rt, fc)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			fmt.Fprintf(ios.ErrOut, "Warning: no compute results for %s, skipping\n", t)
			continue
		case err != nil:
			return nil, fmt.Errorf("forecast %s: %w", t, err)
		}
		allResults = append(allResults, rf.Results...)
		forecasted = true

		if opts.Exporter == nil {
			if err := renderBaselineTable(ios, rt, rf.Area, fc.InitialPCI, rf.Baseline, rf.TotalDeferred, years, sys); err != nil {
				return nil, err
			}
		}
		if opts.Scenarios && opts.Exporter == nil {
			if err := renderScenarioComparisons(ios, rf.Baseline, rf.Cohorts, years, rf.Params); err != nil {
				return nil, err
			}
		}
	}

	// Every resource was skipped (none computed) — signal exit 3 rather than
	// succeeding with an empty forecast.
	if !forecasted {
		return nil, cmdutil.ErrNoResults
	}

	return allResults, nil
}
