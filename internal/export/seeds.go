package export

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// CohortSeed holds per-cohort data for interactive controls.
type CohortSeed struct {
	Classification string  `json:"classification"`
	Area           float64 `json:"area"`
	DecayRate      float64 `json:"decay_rate"`
}

// ForecastSeedJSON holds the data needed by the browser to initialize interactive controls.
// CityPaved is the city-jurisdiction paved area, NOT the city boundary area
// (that's MetaJSON.CityArea). The fields used to share the json tag
// "city_area" with 14x divergence; never reintroduce that name here.
type ForecastSeedJSON struct {
	InitialPCI  float64             `json:"initial_pci"`
	DecayRate   float64             `json:"decay_rate"`
	GrowthRate  float64             `json:"growth_rate"`
	Years       int                 `json:"years"`
	TotalArea   float64             `json:"total_area"`
	CityPaved   float64             `json:"city_paved"`
	CostTiers   []forecast.CostTier `json:"cost_tiers"`
	Cohorts     []CohortSeed        `json:"cohorts,omitempty"`
	CityCohorts []CohortSeed        `json:"city_cohorts,omitempty"`
}

// BuildForecastSeed constructs a ForecastSeedJSON for the given forecast config and store.
func BuildForecastSeed(ctx context.Context, fc *config.ForecastConfig, store db.Store) (template.JS, error) {
	costTiers := ConvertCostTiers(fc)
	if len(costTiers) == 0 {
		costTiers = forecast.DefaultCostTiers
	}

	// Prefer the cross-resource union rows (RunCombined). Fall back to summing
	// per-resource rows when missing — same behavior as BuildMeta.
	var totalArea, cityArea float64
	if r, err := store.LatestComputeResult(ctx, resource.CombinedAll); err == nil && r != nil {
		totalArea = r.TotalArea
	} else {
		for _, rt := range resource.All {
			result, err := store.LatestComputeResult(ctx, rt.Type())
			if err != nil || result == nil {
				continue
			}
			totalArea += result.TotalArea
		}
	}
	if r, err := store.LatestComputeResult(ctx, resource.CombinedCity); err == nil && r != nil {
		cityArea = r.TotalArea
	} else {
		for _, rt := range resource.All {
			cityResult, err := store.LatestComputeResult(ctx, rt.Type().With(resource.ScopeCity))
			if err == nil && cityResult != nil {
				cityArea += cityResult.TotalArea
			}
		}
	}

	decayRate := fc.DecayRate
	if decayRate <= 0 {
		decayRate = forecast.DefaultDecayRates["default"]
	}

	years := fc.Years

	// Collect cohort stats
	cohortSeeds, cityCohortSeeds := collectCohortSeeds(ctx, store, fc)

	seed := ForecastSeedJSON{
		InitialPCI:  fc.InitialPCI,
		DecayRate:   decayRate,
		GrowthRate:  fc.GrowthRate,
		Years:       years,
		TotalArea:   totalArea,
		CityPaved:   cityArea,
		CostTiers:   costTiers,
		Cohorts:     cohortSeeds,
		CityCohorts: cityCohortSeeds,
	}
	data, err := json.Marshal(seed)
	if err != nil {
		return "", fmt.Errorf("marshal forecast seed: %w", err)
	}
	return template.JS(data), nil
}

// collectCohortSeeds iterates over all resource types and collects cohort seed
// data for both the main and city-scoped cohort stats.
func collectCohortSeeds(ctx context.Context, store db.Store, fc *config.ForecastConfig) ([]CohortSeed, []CohortSeed) {
	var cohortSeeds []CohortSeed
	var cityCohortSeeds []CohortSeed
	for _, rt := range resource.All {
		t := rt.Type()
		stats, err := store.ListCohortStats(ctx, t)
		if err == nil {
			for _, st := range stats {
				cohortSeeds = append(cohortSeeds, CohortSeed{
					Classification: st.Classification,
					Area:           st.Area,
					DecayRate:      resolvedDecayRate(st.Classification, fc.DecayRate),
				})
			}
		}
		cityStats, err := store.ListCohortStats(ctx, t.With(resource.ScopeCity))
		if err == nil {
			for _, st := range cityStats {
				cityCohortSeeds = append(cityCohortSeeds, CohortSeed{
					Classification: st.Classification,
					Area:           st.Area,
					DecayRate:      resolvedDecayRate(st.Classification, fc.DecayRate),
				})
			}
		}
	}
	return cohortSeeds, cityCohortSeeds
}

// resolvedDecayRate returns the decay rate for a classification, applying the
// config override only to road classes.
func resolvedDecayRate(classification string, overrideRate float64) float64 {
	rate := forecast.DecayRateForClass(classification)
	if overrideRate > 0 && forecast.IsRoadClass(classification) {
		rate = overrideRate
	}
	return rate
}
