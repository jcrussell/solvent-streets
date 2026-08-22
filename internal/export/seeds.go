package export

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	InitialPCI float64 `json:"initial_pci"`
	DecayRate  float64 `json:"decay_rate"`
	GrowthRate float64 `json:"growth_rate"`
	Years      int     `json:"years"`
	// TreatmentCycleYears is shipped resolved (default applied) so the browser's
	// interactive "Custom Scenario" line uses the same cycle N as the static
	// export lines; otherwise the custom line runs at a different N and the two
	// diverge ~N× on the same chart. Key matches bridge.Input.CycleYears.
	TreatmentCycleYears float64 `json:"treatment_cycle_years"`
	TotalArea           float64 `json:"total_area"`
	CityPaved           float64 `json:"city_paved"`
	// CostTiers ships the BARE per-m2 schedule, unscaled by CostOverhead. The
	// browser renders these into the editable #tier-inputs, so scaling them
	// here would show the user loaded prices in boxes labelled with the tier's
	// own cost — and the WASM bridge would then apply CostOverhead on top,
	// pricing everything at overhead^2. Tiers and overhead travel separately
	// and are combined exactly once, in TieredCostProjector.
	CostTiers []forecast.CostTier `json:"cost_tiers"`
	// CostOverhead is the resolved loaded-cost multiplier (ADA + soft costs +
	// contingency; config.DefaultCostOverhead). Shipped resolved for the same
	// reason as TreatmentCycleYears: the browser's interactive line must price
	// work the same way the static export lines did, or the two diverge by this
	// factor on the same chart. It also seeds the forecast page's overhead
	// slider. Key matches bridge.Input.CostOverhead.
	CostOverhead float64 `json:"cost_overhead"`
	// MaterialTiers ships the per-tier physical material intensities (asphalt
	// mix mass + binder fraction per m^2) that the Materials tab multiplies by
	// each year's treated area (area / treatment_cycle_years) to estimate annual
	// asphalt/binder/oil demand. Aligned label-for-label with CostTiers.
	MaterialTiers []forecast.MaterialTier `json:"material_tiers,omitempty"`
	// BarrelsPerTonBinder is the crude-oil-equivalent barrels per tonne of
	// asphalt binder, shipped so the Materials tab's oil figures use the same
	// factor as the Go model (forecast.BarrelsPerTonBinder) rather than a
	// duplicated JS constant that could drift.
	BarrelsPerTonBinder float64      `json:"barrels_per_ton_binder,omitempty"`
	Cohorts             []CohortSeed `json:"cohorts,omitempty"`
	CityCohorts         []CohortSeed `json:"city_cohorts,omitempty"`
	// AsphaltAreaShare and CityAsphaltAreaShare are the fraction of each
	// scope's cohort area that is actually flexible (asphalt) pavement, i.e.
	// roads and parking but not sidewalks. They exist because MaterialTiers
	// describes asphalt only — forecast/material.go says outright that
	// sidewalks are concrete — while the scenario areas the Materials tab
	// multiplies by those tiers sum ALL of resource.All. Without the netting,
	// "Annual Binder (Oil)" attributes crude-oil-equivalent barrels to concrete
	// sidewalk panels that consume zero bitumen, overstating the mix/binder/oil
	// headlines by the sidewalk share (1.4% alameda-ca, 7.0% atlanta-ga, 8.9%
	// austin-tx in a real 277-city export).
	//
	// They are a SHARE and not an area because the browser applies them to
	// scenario.years[].area, which is the cohort-summed area — not the deduped
	// union geometry behind TotalArea/CityPaved. Same scope-selection shape as
	// that pair. 1.0 means "no netting" and is what an absent or empty cohort
	// set resolves to, so a fresh DB behaves exactly as before.
	//
	// Not omitempty: a legitimately-computed share is never 0 (a city with only
	// sidewalks has no scenarios to draw), so an absent key means an older
	// export and the browser defaults to 1.
	AsphaltAreaShare     float64 `json:"asphalt_area_share"`
	CityAsphaltAreaShare float64 `json:"city_asphalt_area_share"`
}

// BuildForecastSeed constructs a ForecastSeedJSON for the given forecast config and store.
func BuildForecastSeed(ctx context.Context, fc *config.ForecastConfig, store db.Store) (template.JS, error) {
	costTiers := ConvertCostTiers(fc)
	if len(costTiers) == 0 {
		costTiers = forecast.DefaultCostTiers
	}
	// Deliberately NOT scaled by the overhead — see the CostTiers field comment.
	costOverhead := fc.ResolvedCostOverhead()

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
	cohorts, err := collectCohortSeeds(ctx, store, fc)
	if err != nil {
		return "", err
	}

	seed := ForecastSeedJSON{
		InitialPCI:           fc.InitialPCI,
		DecayRate:            decayRate,
		GrowthRate:           fc.GrowthRate,
		Years:                years,
		TreatmentCycleYears:  forecast.ResolveCycleYears(fc.TreatmentCycleYears),
		TotalArea:            totalArea,
		CityPaved:            cityArea,
		CostTiers:            costTiers,
		CostOverhead:         costOverhead,
		MaterialTiers:        forecast.DefaultMaterialTiers,
		BarrelsPerTonBinder:  forecast.BarrelsPerTonBinder,
		Cohorts:              cohorts.BBox,
		CityCohorts:          cohorts.City,
		AsphaltAreaShare:     cohorts.BBoxAsphaltShare,
		CityAsphaltAreaShare: cohorts.CityAsphaltShare,
	}
	data, err := json.Marshal(seed)
	if err != nil {
		return "", fmt.Errorf("marshal forecast seed: %w", err)
	}
	return template.JS(data), nil
}

// cohortSet is what one store's cohort stats resolve to: the per-scope seeds
// the interactive line and the static scenarios both build from, plus the
// asphalt share of each scope's total cohort area (see ForecastSeedJSON's
// AsphaltAreaShare).
type cohortSet struct {
	BBox, City                         []CohortSeed
	BBoxAsphaltShare, CityAsphaltShare float64
}

// areaSplit accumulates asphalt-vs-total cohort area for one scope.
type areaSplit struct{ asphalt, total float64 }

// share returns the asphalt fraction, or 1 when there is no area to split.
// 1 is the identity for the browser's multiplication, so a store with no
// cohorts behaves exactly as it did before the netting existed — including the
// fresh-DB path where scenariosFromSeeds returns nil and BuildScenariosData
// falls back to singleCohortScenarios over the aggregate compute area.
func (a *areaSplit) share() float64 {
	if a.total <= 0 {
		return 1
	}
	return a.asphalt / a.total
}

func (a *areaSplit) add(area float64, asphalt bool) {
	a.total += area
	if asphalt {
		a.asphalt += area
	}
}

// collectCohortSeeds iterates over all resource types and collects cohort seed
// data for both the main and city-scoped cohort stats. sql.ErrNoRows for a
// given type is the normal "no cohorts computed for this resource" state and is
// tolerated (that type contributes nothing); any other ListCohortStats error is
// a real DB failure and is returned so the server's scenarios/seed cache evicts
// and retries instead of locking in a synthetic-cohort payload for the server's
// lifetime.
//
// The asphalt shares are accumulated in this same loop, and deliberately so:
// they have to be computed on the COHORT area basis, because that is what
// scenario.years[].area sums. The combined compute rows behind TotalArea are
// deduped union geometry — a different basis — and a share taken from those
// would leave a residual error in the Materials figures.
func collectCohortSeeds(ctx context.Context, store db.Store, fc *config.ForecastConfig) (cohortSet, error) {
	var out cohortSet
	var bboxSplit, citySplit areaSplit
	for _, rt := range resource.All {
		t := rt.Type()
		asphalt := rt.AsphaltSurfaced()
		stats, err := store.ListCohortStats(ctx, t)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return cohortSet{}, fmt.Errorf("list cohort stats for %s: %w", t, err)
		}
		for _, st := range stats {
			bboxSplit.add(st.Area, asphalt)
			out.BBox = append(out.BBox, CohortSeed{
				Classification: st.Classification,
				Area:           st.Area,
				DecayRate:      resolvedDecayRate(st.Classification, fc.DecayRate),
			})
		}
		cityStats, err := store.ListCohortStats(ctx, t.With(resource.ScopeCity))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return cohortSet{}, fmt.Errorf("list cohort stats for %s: %w", t.With(resource.ScopeCity), err)
		}
		for _, st := range cityStats {
			citySplit.add(st.Area, asphalt)
			out.City = append(out.City, CohortSeed{
				Classification: st.Classification,
				Area:           st.Area,
				DecayRate:      resolvedDecayRate(st.Classification, fc.DecayRate),
			})
		}
	}
	out.BBoxAsphaltShare = bboxSplit.share()
	out.CityAsphaltShare = citySplit.share()
	return out, nil
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
