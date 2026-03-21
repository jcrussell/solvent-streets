package forecast

import (
	"fmt"

	"pvmt/internal/config"
	"pvmt/internal/db"
	fcpkg "pvmt/internal/forecast"
	"pvmt/internal/geo"
	"pvmt/internal/resource"
	"pvmt/pkg/cmdutil"
	"pvmt/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Options struct {
	IO        *iostreams.IOStreams
	DB        func() (db.Store, error)
	Config    func() (*config.Config, error)
	Scenarios bool
	Exporter  cmdutil.Exporter
}

var forecastFields = []string{"resourceType", "year", "pci", "areaSqFt", "treatmentCost", "treatmentTier"}

func NewCmdForecast(f *cmdutil.Factory, runF func(*Options) error) *cobra.Command {
	opts := &Options{
		IO:     f.IOStreams,
		DB:     f.DB,
		Config: f.Config,
	}

	cmd := &cobra.Command{
		Use:   "forecast",
		Short: "Project pavement deterioration and maintenance costs",
		Long:  "Run PCI decay and cost projections over a configurable time horizon.\nShows projected deterioration and deferred maintenance costs.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(opts)
			}
			return runForecast(opts)
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
			AreaSqFt:      y.AreaSqFt,
			TreatmentCost: y.AnnualNeed,
			TreatmentTier: y.CostTier,
		})
	}

	return results, totalDeferredCost, baseline
}

func runForecast(opts *Options) error {
	ios := opts.IO

	cfg, err := opts.Config()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	store, err := opts.DB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	years := cfg.ForecastYears()

	var costTiers []fcpkg.CostTier
	for _, t := range cfg.Forecast.CostTiers {
		costTiers = append(costTiers, fcpkg.CostTier{
			MinPCI:      t.MinPCI,
			MaxPCI:      t.MaxPCI,
			CostPerSqFt: t.CostPerSqFt,
			Label:       t.Label,
		})
	}

	var allResults []db.ForecastResult

	fmt.Fprintf(ios.Out, "Running %d-year forecast...\n\n", years)

	for _, rt := range resource.All {
		params := fcpkg.NewParamsForResource(rt.Name(), cfg.Forecast.GrowthRate, costTiers)
		result, err := store.LatestComputeResult(rt.Name())
		if err != nil || result == nil {
			fmt.Fprintf(ios.ErrOut, "Warning: no compute results for %s, skipping\n", rt.Name())
			continue
		}

		areaSqFt := result.TotalAreaSqFt
		currentPCI := 85.0 // assume good initial condition

		// Build cohorts from stored cohort stats
		stats, _ := store.ListCohortStats(rt.Name())
		var inputs []fcpkg.CohortInput
		for _, st := range stats {
			inputs = append(inputs, fcpkg.CohortInput{
				Classification: st.Classification,
				AreaSqFt:       st.AreaSqFt,
			})
		}
		cohorts := fcpkg.BuildCohorts(inputs, currentPCI, cfg.Forecast.DecayRate)
		if cohorts == nil {
			defaultRate := fcpkg.DecayRateForClass(rt.Name())
			if cfg.Forecast.DecayRate > 0 {
				defaultRate = cfg.Forecast.DecayRate
			}
			cohorts = []fcpkg.Cohort{{
				Classification: rt.Name(),
				AreaSqFt:       areaSqFt,
				DecayRate:      defaultRate,
				InitialPCI:     currentPCI,
			}}
		}

		dbResults, totalDeferredCost, baseline := simulateResource(rt, cohorts, years, params)
		allResults = append(allResults, dbResults...)

		// Table output
		if opts.Exporter == nil {
			fmt.Fprintf(ios.Out, "=== %s ===\n", rt.Name())
			fmt.Fprintf(ios.Out, "  Current area: %.0f sq ft (%.1f acres)\n", areaSqFt, geo.AreaAcres(areaSqFt))
			fmt.Fprintf(ios.Out, "  Initial PCI: %.0f\n\n", currentPCI)

			tp := iostreams.NewTablePrinter(ios)
			tp.AddHeader("Year", "PCI", "Area (acres)", "Treatment Cost", "Tier")
			for _, y := range baseline.Years {
				if y.Year <= 5 || y.Year%5 == 0 || y.Year == years {
					tp.AddRow(
						fmt.Sprintf("%d", y.Year),
						fmt.Sprintf("%.1f", y.PCI),
						fmt.Sprintf("%.1f", geo.AreaAcres(y.AreaSqFt)),
						fmt.Sprintf("$%.0f", y.AnnualNeed),
						y.CostTier,
					)
				}
			}
			if err := tp.Render(); err != nil {
				return err
			}

			fmt.Fprintf(ios.Out, "\n  Total %d-year deferred maintenance: $%.0f\n\n", years, totalDeferredCost)

			// Print per-cohort breakdown
			if len(baseline.FinalCohorts) > 1 {
				fmt.Fprintf(ios.Out, "  Cohort Breakdown:\n")
				cp := iostreams.NewTablePrinter(ios)
				cp.AddHeader("Classification", "Area %", "Decay Rate", "End PCI")
				for _, fc := range baseline.FinalCohorts {
					areaPct := 0.0
					if areaSqFt > 0 {
						areaPct = fc.AreaSqFt / areaSqFt * 100
					}
					cp.AddRow(
						fc.Classification,
						fmt.Sprintf("%.1f%%", areaPct),
						fmt.Sprintf("%.3f", fc.DecayRate),
						fmt.Sprintf("%.1f", fc.EndPCI),
					)
				}
				if err := cp.Render(); err != nil {
					return err
				}
				fmt.Fprintln(ios.Out)
			}
		}

		if err := store.SaveForecastResults(dbResults); err != nil {
			fmt.Fprintf(ios.ErrOut, "Warning: failed to save forecast results: %v\n", err)
		}

		// Scenario comparisons (table mode only)
		if opts.Scenarios && opts.Exporter == nil {
			year1Need := baseline.Years[0].AnnualNeed
			comparisons := fcpkg.GroupedComparisons(year1Need, cohorts, years,
				params.Cost, params.Growth)

			for _, comp := range comparisons {
				fmt.Fprintf(ios.Out, "  --- %s ---\n", comp.Title)

				tp := iostreams.NewTablePrinter(ios)
				tp.AddHeader("Scenario", "End PCI", "Annual Budget", "20yr Backlog")
				for _, sr := range comp.Scenarios {
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
			}
		}
	}

	// JSON output
	if opts.Exporter != nil {
		return opts.Exporter.Write(ios, allResults)
	}

	return nil
}
