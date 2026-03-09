package forecast

import (
	"fmt"

	"pvmt/internal/db"
	fcpkg "pvmt/internal/forecast"
	"pvmt/internal/geo"
	"pvmt/internal/resource"
	"pvmt/pkg/cmdutil"

	"github.com/spf13/cobra"
)

type Options struct {
	Factory   *cmdutil.Factory
	Scenarios bool
}

func NewCmdForecast(f *cmdutil.Factory, runF func(*Options) error) *cobra.Command {
	opts := &Options{Factory: f}

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

	return cmd
}

func runForecast(opts *Options) error {
	ios := opts.Factory.IOStreams

	cfg, err := opts.Factory.Config()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	store, err := opts.Factory.DB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	years := cfg.ForecastYears()

	// Build shared forecasting params from config (DRY)
	var costTiers []fcpkg.CostTier
	for _, t := range cfg.Forecast.CostTiers {
		costTiers = append(costTiers, fcpkg.CostTier{
			MinPCI:      t.MinPCI,
			MaxPCI:      t.MaxPCI,
			CostPerSqFt: t.CostPerSqFt,
			Label:       t.Label,
		})
	}
	params := fcpkg.NewParams(cfg.Forecast.DecayRate, cfg.Forecast.GrowthRate, costTiers)

	fmt.Fprintf(ios.Out, "Running %d-year forecast...\n\n", years)

	for _, rt := range resource.All {
		result, err := store.LatestComputeResult(rt.Name())
		if err != nil || result == nil {
			fmt.Fprintf(ios.ErrOut, "Warning: no compute results for %s, skipping\n", rt.Name())
			continue
		}

		areaSqFt := result.TotalAreaSqFt
		currentPCI := 85.0 // assume good initial condition

		// Run baseline simulation using Simulate()
		baseline := fcpkg.Simulate(
			fcpkg.Scenario{Name: "baseline", Label: "Baseline (Do Nothing)", Strategy: fcpkg.StrategyDoNothing},
			areaSqFt, currentPCI, years, params.PCI, params.Cost, params.Growth,
		)

		fmt.Fprintf(ios.Out, "=== %s ===\n", rt.Name())
		fmt.Fprintf(ios.Out, "  Current area: %.0f sq ft (%.1f acres)\n", areaSqFt, geo.AreaAcres(areaSqFt))
		fmt.Fprintf(ios.Out, "  Initial PCI: %.0f\n\n", currentPCI)
		fmt.Fprintf(ios.Out, "  %-6s  %-6s  %-14s  %-16s  %s\n", "Year", "PCI", "Area (acres)", "Treatment Cost", "Tier")
		fmt.Fprintf(ios.Out, "  %s\n", "------  ------  --------------  ----------------  ----------------")

		var forecastResults []db.ForecastResult
		var totalDeferredCost float64

		for _, y := range baseline.Years {
			totalDeferredCost += y.AnnualNeed

			forecastResults = append(forecastResults, db.ForecastResult{
				ResourceType:  rt.Name(),
				Year:          y.Year,
				PCI:           y.PCI,
				AreaSqFt:      y.AreaSqFt,
				TreatmentCost: y.AnnualNeed,
				TreatmentTier: y.CostTier,
			})

			if y.Year <= 5 || y.Year%5 == 0 || y.Year == years {
				fmt.Fprintf(ios.Out, "  %-6d  %-6.1f  %-14.1f  $%-15.0f  %s\n",
					y.Year, y.PCI, geo.AreaAcres(y.AreaSqFt), y.AnnualNeed, y.CostTier)
			}
		}

		fmt.Fprintf(ios.Out, "\n  Total %d-year deferred maintenance: $%.0f\n\n", years, totalDeferredCost)

		if err := store.SaveForecastResults(forecastResults); err != nil {
			fmt.Fprintf(ios.ErrOut, "Warning: failed to save forecast results: %v\n", err)
		}

		// Scenario comparisons
		if opts.Scenarios {
			year1Need := baseline.Years[0].AnnualNeed
			comparisons := fcpkg.GroupedComparisons(year1Need, areaSqFt, currentPCI, years,
				params.PCI, params.Cost, params.Growth)

			for _, comp := range comparisons {
				fmt.Fprintf(ios.Out, "  --- %s ---\n", comp.Title)
				fmt.Fprintf(ios.Out, "  %-25s  %-8s  %-16s  %s\n", "Scenario", "End PCI", "Annual Budget", "20yr Backlog")
				fmt.Fprintf(ios.Out, "  %s\n", "-------------------------  --------  ----------------  ----------------")
				for _, sr := range comp.Scenarios {
					last := sr.Years[len(sr.Years)-1]
					budgetStr := "unconstrained"
					if sr.Scenario.AnnualBudget > 0 {
						budgetStr = fmt.Sprintf("$%.0f", sr.Scenario.AnnualBudget)
					}
					fmt.Fprintf(ios.Out, "  %-25s  %-8.1f  %-16s  $%.0f\n",
						sr.Scenario.Label, last.PCI, budgetStr, last.DeferredBacklog)
				}
				fmt.Fprintf(ios.Out, "\n")
			}
		}
	}

	return nil
}
