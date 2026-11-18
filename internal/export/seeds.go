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
	"github.com/jcrussell/solvent-streets/internal/results"
)

// CohortSeed holds per-cohort data for interactive controls.
type CohortSeed struct {
	// ResourceType is the resource the cohort belongs to ("roads", "parking",
	// "sidewalks"). The browser selects the roads cohorts by it rather than by
	// guessing from the class name.
	ResourceType   string  `json:"resource_type"`
	Classification string  `json:"classification"`
	Area           float64 `json:"area"`
	DecayRate      float64 `json:"decay_rate"`
}

// ForecastSeedJSON holds the data needed by the browser to initialize interactive controls.
// CityPaved is the city-jurisdiction paved area, NOT the city boundary area
// (that's MetaJSON.CityArea); the two differ by an order of magnitude.
type ForecastSeedJSON struct {
	InitialPCI   float64 `json:"initial_pci"`
	DecayRate    float64 `json:"decay_rate"`
	GrowthRate   float64 `json:"growth_rate"`
	HorizonYears int     `json:"horizon_years"`
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
	// mix mass + binder fraction per m^2) behind each scenario year's materials
	// in scenarios.json, for the Materials tab's intensity table. Each tier
	// carries its own PCI band; it need not match a CostTiers band or label
	// (solvent-streets-ern4.25).
	MaterialTiers []MaterialTierSeed `json:"material_tiers,omitempty"`
	// BarrelsPerTonBinder is the crude-oil-equivalent barrels per tonne of
	// asphalt binder (forecast.BarrelsPerTonBinder), which the Materials tab
	// quotes beside its oil figure.
	BarrelsPerTonBinder float64      `json:"barrels_per_ton_binder,omitempty"`
	Cohorts             []CohortSeed `json:"cohorts,omitempty"`
	CityCohorts         []CohortSeed `json:"city_cohorts,omitempty"`
	// AsphaltAreaShare and CityAsphaltAreaShare are the fraction of each
	// scope's cohort area that is actually flexible (asphalt) pavement, i.e.
	// roads and parking but not sidewalks. MaterialTiers describes asphalt
	// only — forecast/material.go says outright that sidewalks are concrete —
	// while scenario areas sum ALL of resource.All, so BuildScenariosData
	// scales each year's treated area by the scope's share before pricing its
	// materials. Without the netting, "Annual Binder (Oil)" attributes
	// crude-oil-equivalent barrels to concrete sidewalk panels that consume
	// zero bitumen, overstating the mix/binder/oil headlines by the sidewalk
	// share (1.4% alameda-ca, 7.0% atlanta-ga, 8.9% austin-tx in a real
	// 277-city export). The Materials tab reads the share to say how much of
	// the network its figures cover.
	//
	// They are a SHARE and not an area because they apply to
	// scenario.years[].area, which is the cohort-summed area — not the deduped
	// union geometry behind TotalArea/CityPaved. Same scope-selection shape as
	// that pair. 1.0 means "no netting" and is what an absent or empty cohort
	// set resolves to.
	//
	// Not omitempty, and the reason is NOT that 0 is unreachable. A
	// concrete-only network resolves a legitimate 0: buildCohortStats in
	// pkg/cmd/compute emits a synthetic single-cohort row even where
	// HasCohorts() is false, so a sidewalk-only store does produce cohort seeds
	// and scenarios to draw, and the Materials tab renders 0 t mix, 0 bbl oil
	// and three flat-zero charts, which is the right answer for a network that
	// consumes no bitumen. omitempty would elide that real 0 and the tab's
	// note would describe a concrete-only city as all asphalt.
	// solvent-streets-ujji.
	AsphaltAreaShare     float64 `json:"asphalt_area_share"`
	CityAsphaltAreaShare float64 `json:"city_asphalt_area_share"`
}

// BuildForecastSeed constructs a ForecastSeedJSON for the given forecast config and store.
func BuildForecastSeed(ctx context.Context, fc *config.ForecastConfig, store db.Store) (template.JS, error) {
	totalArea, cityArea, err := pavedAreas(ctx, store)
	if err != nil {
		return "", fmt.Errorf("paved area: %w", err)
	}
	cohorts, err := collectCohortSeeds(ctx, store, fc)
	if err != nil {
		return "", err
	}
	return marshalForecastSeed(fc, totalArea, cityArea, cohorts)
}

// marshalForecastSeed is the one place a forecast seed is assembled, for one
// city (BuildForecastSeed) and for a whole config
// (BuildMultiCityForecastSeed): the resolved forecast settings plus the paved
// areas and cohorts the caller read.
func marshalForecastSeed(fc *config.ForecastConfig, totalArea, cityArea float64, cohorts cohortSet) (template.JS, error) {
	costTiers := ConvertCostTiers(fc)
	if len(costTiers) == 0 {
		costTiers = forecast.DefaultCostTiers
	}
	seed := ForecastSeedJSON{
		InitialPCI:          fc.InitialPCI,
		DecayRate:           forecast.ResolveDecayRate(fc.DecayRate),
		GrowthRate:          fc.GrowthRate,
		HorizonYears:        fc.Years,
		TreatmentCycleYears: forecast.ResolveCycleYears(fc.TreatmentCycleYears),
		TotalArea:           totalArea,
		CityPaved:           cityArea,
		// Deliberately NOT scaled by the overhead — see the CostTiers field comment.
		CostTiers:            costTiers,
		CostOverhead:         fc.ResolvedCostOverhead(),
		MaterialTiers:        materialTierSeeds(forecast.DefaultMaterialTiers),
		BarrelsPerTonBinder:  forecast.BarrelsPerTonBinder,
		Cohorts:              cohorts.All,
		CityCohorts:          cohorts.City,
		AsphaltAreaShare:     cohorts.AllAsphaltShare,
		CityAsphaltAreaShare: cohorts.CityAsphaltShare,
	}
	data, err := json.Marshal(seed)
	if err != nil {
		return "", fmt.Errorf("marshal forecast seed: %w", err)
	}
	return template.JS(data), nil
}

// MaterialTierSeed is a material tier with the quantities it takes to treat
// 1000 m^2, as the Materials tab's intensity table shows them. They are not
// rounded: the table shows one decimal, and the preventive tier's binder sits
// on a rounding boundary (1.95) that an earlier rounding would tip.
type MaterialTierSeed struct {
	forecast.MaterialTier
	Per1000SqM forecast.MaterialQuantities `json:"per_1000_sqm"`
}

func materialTierSeeds(tiers []forecast.MaterialTier) []MaterialTierSeed {
	out := make([]MaterialTierSeed, len(tiers))
	for i, t := range tiers {
		out[i] = MaterialTierSeed{MaterialTier: t, Per1000SqM: forecast.MaterialsForArea(1000, t)}
	}
	return out
}

// pavedAreas returns a store's paved area (results.PavedArea) in the
// all-jurisdiction and the city scope: the seed's total_area and city_paved.
func pavedAreas(ctx context.Context, store db.Store) (all, city float64, err error) {
	if all, _, err = results.PavedArea(ctx, store, resource.ScopeAll); err != nil {
		return 0, 0, err
	}
	if city, _, err = results.PavedArea(ctx, store, resource.ScopeCity); err != nil {
		return 0, 0, err
	}
	return all, city, nil
}

// cohortSet is what one store's cohort stats resolve to: the per-scope seeds
// the interactive line and the static scenarios both build from, plus the
// asphalt share of each scope's total cohort area (see ForecastSeedJSON's
// AsphaltAreaShare).
type cohortSet struct {
	All, City                         []CohortSeed
	AllAsphaltShare, CityAsphaltShare float64
}

// areaSplit accumulates asphalt-vs-total cohort area for one scope.
type areaSplit struct{ asphalt, total float64 }

// share returns the asphalt fraction, or 1 when there is no area to split.
// 1 nets nothing out, which is right for a store with no cohorts — including
// the fresh-DB path where scenariosFromSeeds returns nil and BuildScenariosData
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
// tolerated (that type contributes nothing); any other cohort-read error is
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
	var allSplit, citySplit areaSplit
	for _, rt := range resource.All {
		t := rt.Type()
		asphalt := rt.AsphaltSurfaced()
		stats, err := results.LoadCohorts(ctx, store, t)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return cohortSet{}, fmt.Errorf("list cohort stats for %s: %w", t, err)
		}
		for _, st := range stats {
			allSplit.add(st.Area, asphalt)
			out.All = append(out.All, CohortSeed{
				ResourceType:   string(t),
				Classification: st.Class,
				Area:           st.Area,
				DecayRate:      forecast.ClassDecayRate(st.Class, fc.DecayRate),
			})
		}
		cityStats, err := results.LoadCohorts(ctx, store, t.With(resource.ScopeCity))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return cohortSet{}, fmt.Errorf("list cohort stats for %s: %w", t.With(resource.ScopeCity), err)
		}
		for _, st := range cityStats {
			citySplit.add(st.Area, asphalt)
			out.City = append(out.City, CohortSeed{
				ResourceType:   string(t),
				Classification: st.Class,
				Area:           st.Area,
				DecayRate:      forecast.ClassDecayRate(st.Class, fc.DecayRate),
			})
		}
	}
	out.AllAsphaltShare = allSplit.share()
	out.CityAsphaltShare = citySplit.share()
	return out, nil
}
