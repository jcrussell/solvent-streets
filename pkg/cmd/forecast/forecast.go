package forecast

import (
	"context"
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
}

var forecastFields = []string{"resourceType", "year", "pci", "areaSqM", "treatmentCost", "treatmentTier"}

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
		case "areaSqM":
			out[f] = r.AreaSqM
		case "treatmentCost":
			out[f] = r.TreatmentCost
		case "treatmentTier":
			out[f] = r.TreatmentTier
		}
	}
	return out
}

func NewCmdForecast(f *cmdutil.Factory, runF func(*Options) error) *cobra.Command {
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
				return runF(opts)
			}
			return runForecast(cmd.Context(), opts)
		},
	}

	cmd.Flags().BoolVar(&opts.Scenarios, "scenarios", true, "Run scenario comparisons")
	cmdutil.AddJSONFlags(cmd, &opts.Exporter, forecastFields)

	return cmd
}

// simulateResource runs the baseline forecast for a single resource type, returning
// the collected results, total deferred cost, and scenario result.
func simulateResource(rt resource.ResourceType, cohorts []fcpkg.Cohort, years int, params *fcpkg.Params) ([]db.ForecastResult, float64, fcpkg.ScenarioResult) {
	baseline := fcpkg.Simulate(
		fcpkg.Scenario{Name: "baseline", Label: "Baseline (Do Nothing)", Strategy: fcpkg.StrategyDoNothing},
		cohorts, years, params.Cost, params.Growth,
	)

	var results []db.ForecastResult
	var totalDeferredCost float64

	for _, y := range baseline.Years {
		totalDeferredCost += y.AnnualNeed
		results = append(results, db.ForecastResult{
			ResourceType:  rt.Name(),
			Year:          y.Year,
			PCI:           y.PCI,
			AreaSqM:       y.AreaSqM,
			TreatmentCost: y.AnnualNeed,
			TreatmentTier: y.CostTier,
		})
	}

	return results, totalDeferredCost, baseline
}

func buildForecastCohorts(ctx context.Context, rt resource.ResourceType, areaSqM float64, store db.Store, fc *config.ForecastConfig) ([]fcpkg.Cohort, error) {
	currentPCI := fc.InitialPCI
	stats, err := store.ListCohortStats(ctx, rt.Name())
	if err != nil {
		return nil, fmt.Errorf("list cohort stats for %s: %w", rt.Name(), err)
	}
	var inputs []fcpkg.CohortInput
	for _, st := range stats {
		inputs = append(inputs, fcpkg.CohortInput{
			Classification: st.Classification,
			AreaSqM:        st.AreaSqM,
		})
	}
	cohorts := fcpkg.BuildCohorts(inputs, currentPCI, fc.DecayRate)
	if cohorts == nil {
		defaultRate := fcpkg.DecayRateForClass(rt.Name())
		if fc.DecayRate > 0 && fcpkg.IsRoadClass(rt.Name()) {
			defaultRate = fc.DecayRate
		}
		cohorts = []fcpkg.Cohort{{
			Classification: rt.Name(),
			AreaSqM:        areaSqM,
			DecayRate:      defaultRate,
			InitialPCI:     currentPCI,
		}}
	}
	return cohorts, nil
}

func renderBaselineTable(ios *iostreams.IOStreams, rt resource.ResourceType, areaSqM, currentPCI float64,
	baseline fcpkg.ScenarioResult, totalDeferredCost float64, years int, sys units.System) error {
	fmt.Fprintf(ios.ErrOut, "=== %s ===\n", rt.Name())
	fmt.Fprintf(ios.ErrOut, "  Current area: %s (%s)\n", units.FormatArea(areaSqM, sys), units.FormatAreaLarge(areaSqM, sys))
	fmt.Fprintf(ios.ErrOut, "  Initial PCI: %.0f\n\n", currentPCI)

	tp := iostreams.NewTablePrinter(ios)
	tp.AddHeader("Year", "PCI", units.AreaLargeLabel(sys), "Treatment Cost", "Tier")
	for _, y := range baseline.Years {
		if y.Year <= 5 || y.Year%5 == 0 || y.Year == years {
			tp.AddRow(
				strconv.Itoa(y.Year),
				fmt.Sprintf("%.1f", y.PCI),
				fmt.Sprintf("%.1f", units.AreaLargeValue(y.AreaSqM, sys)),
				fmt.Sprintf("$%.0f", y.AnnualNeed),
				y.CostTier,
			)
		}
	}
	if err := tp.Render(); err != nil {
		return err
	}
	fmt.Fprintf(ios.ErrOut, "\n  Total %d-year deferred maintenance: $%.0f\n\n", years, totalDeferredCost)

	if len(baseline.FinalCohorts) > 1 {
		fmt.Fprintf(ios.ErrOut, "  Cohort Breakdown:\n")
		cp := iostreams.NewTablePrinter(ios)
		cp.AddHeader("Classification", "Area %", "Decay Rate", "End PCI")
		for _, c := range baseline.FinalCohorts {
			areaPct := 0.0
			if areaSqM > 0 {
				areaPct = c.AreaSqM / areaSqM * 100
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
		fmt.Fprintln(ios.ErrOut)
	}
	return nil
}

func renderScenarioComparisons(ios *iostreams.IOStreams, baseline fcpkg.ScenarioResult,
	cohorts []fcpkg.Cohort, years int, params *fcpkg.Params) error {
	if len(baseline.Years) == 0 {
		return nil
	}
	year1Need := baseline.Years[0].AnnualNeed
	scenarios := fcpkg.SimulateDefaults(year1Need, cohorts, years,
		params.Cost, params.Growth)

	fmt.Fprintf(ios.ErrOut, "  Funding Levels:\n")
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
	fmt.Fprintln(ios.ErrOut)
	return nil
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
	years := fc.Years
	costTiers := convertCostTiers(&fc)
	sys := opts.UnitSystem()

	fmt.Fprintf(ios.ErrOut, "Running %d-year forecast for %s...\n\n", years, city.Name)

	allResults, err := forecastAllResources(ctx, opts, store, &fc, years, costTiers, sys)
	if err != nil {
		return err
	}

	if opts.Exporter != nil {
		rows := make([]forecastRow, len(allResults))
		for i, r := range allResults {
			rows[i] = forecastRow{r}
		}
		return cmdutil.WriteRows(ios, opts.Exporter, rows)
	}
	return nil
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

func forecastAllResources(ctx context.Context, opts *Options, store db.Store,
	fc *config.ForecastConfig, years int, costTiers []fcpkg.CostTier, sys units.System) ([]db.ForecastResult, error) {
	ios := opts.IO
	currentPCI := fc.InitialPCI
	var allResults []db.ForecastResult

	for _, rt := range resource.All {
		params := fcpkg.NewParamsForResource(rt.Name(), fc.GrowthRate, costTiers)
		result, err := store.LatestComputeResult(ctx, rt.Name())
		if err != nil || result == nil {
			fmt.Fprintf(ios.ErrOut, "Warning: no compute results for %s, skipping\n", rt.Name())
			continue
		}

		areaSqM := result.TotalAreaSqM
		cohorts, err := buildForecastCohorts(ctx, rt, areaSqM, store, fc)
		if err != nil {
			return nil, err
		}
		dbResults, totalDeferredCost, baseline := simulateResource(rt, cohorts, years, params)
		allResults = append(allResults, dbResults...)

		if opts.Exporter == nil {
			if err := renderBaselineTable(ios, rt, areaSqM, currentPCI, baseline, totalDeferredCost, years, sys); err != nil {
				return nil, err
			}
		}

		if err := store.SaveForecastResults(ctx, dbResults); err != nil {
			return nil, fmt.Errorf("saving forecast results for %s: %w", rt.Name(), err)
		}

		if opts.Scenarios && opts.Exporter == nil {
			if err := renderScenarioComparisons(ios, baseline, cohorts, years, params); err != nil {
				return nil, err
			}
		}
	}

	return allResults, nil
}
