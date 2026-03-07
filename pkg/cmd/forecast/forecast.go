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
	Factory *cmdutil.Factory
}

func NewCmdForecast(f *cmdutil.Factory) *cobra.Command {
	opts := &Options{Factory: f}

	cmd := &cobra.Command{
		Use:   "forecast",
		Short: "Project pavement deterioration and maintenance costs",
		Long:  "Run PCI decay and cost projections over a configurable time horizon.\nShows projected deterioration and deferred maintenance costs.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runForecast(opts)
		},
	}

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

	pciForecaster := &fcpkg.ExponentialPCIForecaster{
		DecayRate: cfg.Forecast.DecayRate,
	}

	costProjector := &fcpkg.TieredCostProjector{}
	if len(cfg.Forecast.CostTiers) > 0 {
		tiers := make([]fcpkg.CostTier, len(cfg.Forecast.CostTiers))
		for i, t := range cfg.Forecast.CostTiers {
			tiers[i] = fcpkg.CostTier{
				MinPCI:      t.MinPCI,
				MaxPCI:      t.MaxPCI,
				CostPerSqFt: t.CostPerSqFt,
				Label:       t.Label,
			}
		}
		costProjector.Tiers = tiers
	}

	growthEstimator := &fcpkg.LinearGrowthEstimator{
		AnnualGrowthRate: cfg.Forecast.GrowthRate,
	}

	fmt.Fprintf(ios.Out, "Running %d-year forecast...\n\n", years)

	for _, rt := range resource.All {
		result, err := store.LatestComputeResult(rt.Name())
		if err != nil || result == nil {
			fmt.Fprintf(ios.ErrOut, "Warning: no compute results for %s, skipping\n", rt.Name())
			continue
		}

		areaSqFt := result.TotalAreaSqFt
		currentPCI := 85.0 // assume good initial condition

		pciValues := pciForecaster.Forecast(currentPCI, years)
		areaValues := growthEstimator.EstimateGrowth(areaSqFt, years)

		var forecastResults []db.ForecastResult
		var totalDeferredCost float64

		fmt.Fprintf(ios.Out, "=== %s ===\n", rt.Name())
		fmt.Fprintf(ios.Out, "  Current area: %.0f sq ft (%.1f acres)\n", areaSqFt, geo.AreaAcres(areaSqFt))
		fmt.Fprintf(ios.Out, "  Initial PCI: %.0f\n\n", currentPCI)
		fmt.Fprintf(ios.Out, "  %-6s  %-6s  %-14s  %-16s  %s\n", "Year", "PCI", "Area (acres)", "Treatment Cost", "Tier")
		fmt.Fprintf(ios.Out, "  %s\n", "------  ------  --------------  ----------------  ----------------")

		for i := 0; i < years; i++ {
			year := i + 1
			pci := pciValues[i]
			area := areaValues[i]
			cost := costProjector.ProjectCost(area, pci)
			tier := fcpkg.TierForPCI(pci)
			totalDeferredCost += cost

			forecastResults = append(forecastResults, db.ForecastResult{
				ResourceType:  rt.Name(),
				Year:          year,
				PCI:           pci,
				AreaSqFt:      area,
				TreatmentCost: cost,
				TreatmentTier: tier,
			})

			if year <= 5 || year%5 == 0 || year == years {
				fmt.Fprintf(ios.Out, "  %-6d  %-6.1f  %-14.1f  $%-15.0f  %s\n",
					year, pci, geo.AreaAcres(area), cost, tier)
			}
		}

		fmt.Fprintf(ios.Out, "\n  Total %d-year deferred maintenance: $%.0f\n\n", years, totalDeferredCost)

		if err := store.SaveForecastResults(forecastResults); err != nil {
			fmt.Fprintf(ios.ErrOut, "Warning: failed to save forecast results: %v\n", err)
		}
	}

	return nil
}
