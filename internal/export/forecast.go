package export

import (
	"context"
	"errors"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
)

// ConvertCostTiers converts config cost tiers to forecast cost tiers.
func ConvertCostTiers(fc *config.ForecastConfig) []forecast.CostTier {
	var tiers []forecast.CostTier
	for _, t := range fc.CostTiers {
		tiers = append(tiers, forecast.CostTier{
			MinPCI:     t.MinPCI,
			MaxPCI:     t.MaxPCI,
			CostPerSqM: t.CostPerSqM,
			Label:      t.Label,
		})
	}
	return tiers
}

// ForecastScopeCity and ForecastScopeAll name the pavement a forecast covers,
// as scenarios.json keys its scopes: city-maintained, or every jurisdiction.
const (
	ForecastScopeCity = "city"
	ForecastScopeAll  = "all"
)

// ScopedCohorts is what one resource is forecast over in a scope: its area
// and cohorts. Computed is false when the resource has no compute result.
type ScopedCohorts struct {
	Area     float64
	Cohorts  []forecast.Cohort
	Computed bool
}

// BuildScopedCohorts returns every resource's cohorts in the scope the
// dashboard's headline forecasts (BuildForecastsForCity): city-maintained
// (resource.ScopeCity) when the city has any city-scope cohorts, every
// jurisdiction otherwise, or every jurisdiction whenever all is set. The
// scope it used comes back as ForecastScopeCity or ForecastScopeAll. A resource the city maintains
// none of has no city-scope cohorts, as on the dashboard. A non-nil error is
// a real DB failure.
func BuildScopedCohorts(ctx context.Context, store db.Store, fc *config.ForecastConfig, all bool) (map[resource.Type]ScopedCohorts, string, error) {
	types := make([]resource.Type, 0, 2*len(resource.All))
	for _, rt := range resource.All {
		types = append(types, rt.Type(), rt.Type().With(resource.ScopeCity))
	}
	totals, cohortStats, err := results.Load(ctx, store, types)
	if err != nil {
		return nil, "", fmt.Errorf("loading compute results: %w", err)
	}
	scope := ForecastScopeAll
	if !all && cityHasScope(cohortStats) {
		scope = ForecastScopeCity
	}
	out := make(map[resource.Type]ScopedCohorts, len(resource.All))
	for _, rt := range resource.All {
		t := rt.Type()
		total, ok := totals[t]
		if !ok {
			out[t] = ScopedCohorts{}
			continue
		}
		if scope == ForecastScopeCity {
			cityT := t.With(resource.ScopeCity)
			out[t] = ScopedCohorts{
				Area:     totals[cityT].Area,
				Cohorts:  forecast.CohortsFromInputs(cohortInputs(cohortStats[cityT]), fc.InitialPCI, fc.DecayRate),
				Computed: true,
			}
			continue
		}
		out[t] = ScopedCohorts{Area: total.Area, Cohorts: buildCohortsFromStats(t, total.Area, cohortStats[t], fc), Computed: true}
	}
	return out, scope, nil
}

// buildCohortsFromStats builds a resource's all-jurisdiction cohorts from its
// stored cohort stats, falling back to one cohort of its whole area when it
// has no class split.
func buildCohortsFromStats(t resource.Type, area float64, stats []results.Cohort, fc *config.ForecastConfig) []forecast.Cohort {
	inputs := cohortInputs(stats)
	if len(inputs) == 0 {
		// No per-class stats yet: one synthetic cohort of the resource's whole
		// area, decaying at its class rate under the override.
		inputs = []forecast.CohortInput{{Classification: string(t), Area: area}}
	}
	// CohortsFromInputs spreads the single configured mean PCI into a condition
	// distribution so the cost figures reflect the failed/poor tail
	// (docs/validation.md §4); every downstream consumer (baseline, scenarios,
	// break-even, insolvency) sees the same corrected cohorts.
	return forecast.CohortsFromInputs(inputs, fc.InitialPCI, fc.DecayRate)
}

// cohortInputs converts stored cohort stats to forecast inputs.
func cohortInputs(stats []results.Cohort) []forecast.CohortInput {
	if len(stats) == 0 {
		return nil
	}
	inputs := make([]forecast.CohortInput, 0, len(stats))
	for _, st := range stats {
		inputs = append(inputs, forecast.CohortInput{
			Classification: st.Class,
			Area:           st.Area,
		})
	}
	return inputs
}

// ForecastExport holds per-resource forecast results.
//
// Baseline is the do-nothing run over every jurisdiction's pavement, and
// CityBaseline the one over city-maintained pavement, present when the city
// has a city-maintained view (the forecast_seed.json cohorts/city_cohorts
// pairing). The primary scope is the city one when CityBaseline is present,
// else all: Scenarios and the solvency metrics are run on it, since that is
// what a city budget covers.
type ForecastExport struct {
	ResourceType string                    `json:"resource_type"`
	Baseline     forecast.ScenarioResult   `json:"baseline"`
	CityBaseline *forecast.ScenarioResult  `json:"city_baseline,omitempty"`
	Scenarios    []forecast.ScenarioResult `json:"scenarios"`

	// Solvency metrics — populated for the roads resource only (a city's
	// paving budget is a street budget, so the headline is about streets).
	// BreakEvenBudget is always computed for roads;
	// TippingPointYear and FundingGap require a configured current_budget and
	// are omitted otherwise. See buildResourceForecast and the methodology doc.
	TippingPointYear *int     `json:"tipping_point_year,omitempty"` // omitted = solvent through the horizon, or no budget configured
	BreakEvenBudget  float64  `json:"break_even_budget,omitempty"`  // smallest constant annual budget that holds the network steady
	CurrentBudget    float64  `json:"current_budget,omitempty"`     // the configured budget the metrics are measured against; omitted when unset
	FundingGap       *float64 `json:"funding_gap,omitempty"`        // (break_even_budget - current_budget)/current_budget; omitted when no budget is configured
	// RoadsArea is the year-1 area of the baseline the solvency figures are
	// measured on (city-maintained roads when the city has them), so a page can
	// show the network the hold-steady budget maintains rather than an
	// all-jurisdiction, all-resource total. Roads only, like the figures.
	RoadsArea float64 `json:"roads_area,omitempty"`
	// InitialPCISource is the config layer the starting condition comes from
	// (config.Config.InitialPCISource's kind: "default", "env" or "file"), so
	// a page can flag figures built on the default PCI.
	// Roads only, like the figures.
	InitialPCISource config.SourceKind `json:"initial_pci_source,omitempty"`

	// totalArea is the resource's stored all-scope total area, and
	// cityTotalArea its city-scope one (hasCityTotal false when it has none):
	// the totals the forecast was built from, which BuildHexCostSummary
	// spreads the year-1 need over. Not emitted.
	totalArea     float64
	cityTotalArea float64
	hasCityTotal  bool
}

// Primary is the baseline of the primary scope: CityBaseline when present,
// else Baseline. Scenarios and the solvency metrics are run on it.
func (fe ForecastExport) Primary() forecast.ScenarioResult {
	if fe.CityBaseline != nil {
		return *fe.CityBaseline
	}
	return fe.Baseline
}

// errSkipResource sentinel: a resource has no compute run yet (or returned
// a nil row), and the caller should silently skip it rather than treat it
// as a real failure. Distinguishes "legitimate empty for this resource"
// from "real DB error" in the buildResourceForecast tristate.
var errSkipResource = errors.New("no compute result for resource")

// BuildForecastsForCity builds per-resource forecast exports for a city.
// Prefers city-scoped data (excluding state/federal roads) as the primary
// scope since that matches what a city budget covers (see ForecastExport).
//
// Returns (forecasts, nil) on success, (nil, err) on any real DB failure.
// A resource with no city totals is treated as "no compute run yet for this
// resource" and silently skipped — that's a legitimate empty state
// for a freshly-bootstrapped city. Real errors across resources are
// aggregated via errors.Join so callers (e.g. server cache thunks) get the
// full picture in one return; any non-nil error discards the partial slice
// so the cache evicts and retries instead of memoizing a partial result.
func BuildForecastsForCity(ctx context.Context, entry CityEntry, fc *config.ForecastConfig, costTiers []forecast.CostTier) ([]ForecastExport, error) {
	// One batched DB roundtrip reads the totals and cohort splits of the
	// 3-resource × 2-scope cross-product, per city.
	latestByType, cohortStats, err := results.Load(ctx, entry.Store, resource.AllTypes(resource.ScopeAll, resource.ScopeCity))
	if err != nil {
		return nil, fmt.Errorf("loading compute results: %w", err)
	}

	var forecasts []ForecastExport
	var errs []error
	for _, rt := range resource.All {
		fe, err := buildResourceForecast(rt, fc, costTiers, latestByType, cohortStats)
		if errors.Is(err, errSkipResource) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if rt.Type() == resource.TypeRoads && entry.Config != nil {
			fe.InitialPCISource = entry.Config.InitialPCISource(&entry.City).Kind
		}
		forecasts = append(forecasts, fe)
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return forecasts, nil
}

// buildResourceForecast builds the forecast for a single resource from
// pre-batched DB results. Returns errSkipResource when the resource has
// no compute run yet — a legitimate skip on a fresh DB.
func buildResourceForecast(rt resource.Source, fc *config.ForecastConfig, costTiers []forecast.CostTier, latestByType map[resource.Type]results.Totals, cohortStats map[resource.Type][]results.Cohort) (ForecastExport, error) {
	t := rt.Type()
	tName := string(t)
	result, ok := latestByType[t]
	if !ok {
		return ForecastExport{}, errSkipResource
	}

	years := fc.HorizonYears
	rtParams := forecast.NewParamsForResource(tName, fc.GrowthRate, costTiers, fc.TreatmentCycleYears, fc.ResolvedCostMultiplier())

	allCohorts := buildCohortsFromStats(t, result.Area, cohortStats[t], fc)

	// The primary baseline is what the city maintains. A city with no
	// city-scope data at all has only the all-jurisdiction view, so that is
	// its primary. In a city that has city scope, a resource with no
	// city-maintained features is one the city maintains none of: its primary
	// forecast is empty (all zeros), not its all-jurisdiction one, the same
	// way scenarios.json and the forecast seed leave it out of the city set.
	primaryCohorts := allCohorts
	hasCityScope := cityHasScope(cohortStats)
	if hasCityScope {
		primaryCohorts = forecast.CohortsFromInputs(cohortInputs(cohortStats[t.With(resource.ScopeCity)]), fc.InitialPCI, fc.DecayRate)
	}

	// The baseline, then the funding levels sized off its year-1 need.
	reference := forecast.CollapseAllBands(forecast.ReferenceScenarios(primaryCohorts, years, rtParams))
	baseline, scenarios := reference[0], reference[1:]

	fe := ForecastExport{
		ResourceType: tName,
		Baseline:     baseline,
		Scenarios:    scenarios,
		totalArea:    result.Area,
	}
	if cityResult, ok := latestByType[t.With(resource.ScopeCity)]; ok {
		fe.cityTotalArea, fe.hasCityTotal = cityResult.Area, true
	}
	if hasCityScope {
		fe.CityBaseline = &baseline
		fe.Baseline = forecast.CollapseBands(forecast.SimulateBaseline(allCohorts, years, rtParams))
	}

	// Roads-only solvency metrics. Computed on primaryCohorts (city-scope when
	// available — what a city budget covers). Break-even is budget-independent;
	// insolvency year and funding gap need a configured current_budget.
	if t == resource.TypeRoads {
		fe.BreakEvenBudget = forecast.BreakEvenBudget(primaryCohorts, years, rtParams, forecast.StrategyWorstFirst)
		if len(baseline.Years) > 0 {
			fe.RoadsArea = baseline.Years[0].Area
		}
		if fc.CurrentBudget > 0 {
			fe.CurrentBudget = fc.CurrentBudget
		}
		// With no streets to maintain there is no gap or insolvency to state.
		if fc.CurrentBudget > 0 && len(primaryCohorts) > 0 {
			currentRun := forecast.Simulate(
				forecast.Scenario{Name: "current-budget", Label: "Current Budget", AnnualBudget: fc.CurrentBudget, Strategy: forecast.StrategyWorstFirst},
				primaryCohorts, years, rtParams,
			)
			if yr, ok := forecast.TippingPointYear(currentRun, rtParams.CycleYears); ok {
				fe.TippingPointYear = &yr
			}
			gap := forecast.FundingGap(fe.BreakEvenBudget, fc.CurrentBudget)
			fe.FundingGap = &gap
		}
	}
	return fe, nil
}

// cityHasScope reports whether the city has a city-maintained view: some
// resource has city-scope cohorts. collectCohortSeeds' City set is non-empty
// under the same condition.
func cityHasScope(cohortStats map[resource.Type][]results.Cohort) bool {
	for _, rt := range resource.All {
		if len(cohortStats[rt.Type().With(resource.ScopeCity)]) > 0 {
			return true
		}
	}
	return false
}

// BuildScenariosData builds the aggregate scenarios JSON structure for a city:
// {"city": [...], "all": [...]}. "city" (city-maintained
// features, what a city budget covers) is present when the city has
// city-scope area; "all" covers every jurisdiction.
//
// The static Baseline/25%/50%/Full lines are built from the SAME multi-cohort
// seeds (per-class decay rates) that drive the interactive "Custom Scenario"
// line via WASM Simulate — see collectCohortSeeds and index.html.tmpl's
// getControlValues — so the static and custom lines agree on the same chart
// at equal settings.
//
// AREA SCOPE: the scenario lines use the cohort-summed area (sum of per-class
// cohort areas), not the aggregate compute area, so the static and custom
// lines share an identical area basis. The aggregate compute areas only decide
// which scopes exist and size the single-cohort fallback.
func BuildScenariosData(ctx context.Context, entry CityEntry, fc *config.ForecastConfig) (map[string]any, error) {
	costTiers := ConvertCostTiers(fc)
	params := forecast.NewParams(fc.GrowthRate, costTiers, fc.TreatmentCycleYears, fc.ResolvedCostMultiplier())
	defaultRate := forecast.ResolveDecayRate(fc.DecayRate)

	areas, err := maintainedAreas(ctx, entry.Store)
	if err != nil {
		return nil, err
	}

	// Same cohort seeds the interactive line uses: all-scope and city-scope.
	cohorts, err := collectCohortSeeds(ctx, entry.Store, fc)
	if err != nil {
		return nil, err
	}
	allSeeds, citySeeds := cohorts.All, cohorts.City

	// all scenarios from all cohorts; fall back to a single synthetic cohort
	// over the aggregate all area when no cohort stats exist (fresh DB).
	allScenarios := scenariosFromSeeds(allSeeds, fc.InitialPCI, fc.HorizonYears, params)
	if allScenarios == nil {
		allScenarios = singleCohortScenarios("all", areas.allArea, fc.InitialPCI, defaultRate, fc.HorizonYears, params)
	}

	primaryScenarios := allScenarios
	if areas.cityArea > 0 {
		primaryScenarios = scenariosFromSeeds(citySeeds, fc.InitialPCI, fc.HorizonYears, params)
		if primaryScenarios == nil {
			primaryScenarios = singleCohortScenarios("city", areas.cityArea, fc.InitialPCI, defaultRate, fc.HorizonYears, params)
		}
	}

	// Each scope's material demand nets out its own concrete share. With no
	// cohorts the share is 1, which suits the single-cohort fallback.
	allScenarios = withMaterials(allScenarios, cohorts.AllAsphaltShare)

	out := map[string]any{}
	if areas.cityArea > 0 {
		out["city"] = withMaterials(primaryScenarios, cohorts.CityAsphaltShare)
		out["all"] = allScenarios
	} else {
		out["all"] = allScenarios
	}
	return out, nil
}

// withMaterials adds the asphalt each scenario year's spending lays
// (forecast.WithMaterials) at the default material tiers.
func withMaterials(rs []forecast.ScenarioResult, asphaltShare float64) []forecast.ScenarioResult {
	if rs == nil {
		return nil
	}
	out := make([]forecast.ScenarioResult, len(rs))
	for i, r := range rs {
		out[i] = forecast.WithMaterials(r, forecast.DefaultMaterialTiers, asphaltShare)
	}
	return out
}

// scenarioAreas pairs the all-scope and city-scope maintained areas.
type scenarioAreas struct {
	allArea, cityArea float64
}

// maintainedAreas is a store's maintained area in both scopes: the paved
// area summed across resources, so the overlap between a road and the
// sidewalk or lot beside it counts once per resource, as the forecast prices
// it. It equals the cohort sum (each resource's cohorts split its total). It
// is what the forecast seed's maintained_area / city_maintained_area carry
// and what sizes the single-cohort fallback, and deliberately not
// results.PavedArea, the deduplicated footprint behind meta.json's
// total_paved and paved_share. The city-scope lookup is gated on all-scope
// row existence for the same resource — a resource with no all-scope row
// contributes to neither total. One batched DB call collects both scopes.
func maintainedAreas(ctx context.Context, store results.CityMetricLister) (scenarioAreas, error) {
	latestByType, err := results.LoadTotals(ctx, store, resource.AllTypes(resource.ScopeAll, resource.ScopeCity))
	if err != nil {
		// A real DB failure is propagated so serveScenariosJSON's cache
		// evicts and retries instead of locking in a zero-area payload for
		// the server's lifetime. Nothing computed yet is just an empty map.
		return scenarioAreas{}, fmt.Errorf("latest compute results: %w", err)
	}
	var agg scenarioAreas
	for _, rt := range resource.All {
		t := rt.Type()
		result, ok := latestByType[t]
		if !ok {
			continue
		}
		agg.allArea += result.Area

		if cityResult, ok := latestByType[t.With(resource.ScopeCity)]; ok {
			agg.cityArea += cityResult.Area
		}
	}
	return agg, nil
}

// cohortsFromSeeds converts the interactive-line CohortSeeds into forecast
// cohorts. CohortSeed carries no InitialPCI; the interactive path applies the
// single config InitialPCI to every cohort (see internal/forecast/bridge,
// which spreads every seed at input.InitialPCI). Mirror that
// exactly so the static lines track the custom line.
func cohortsFromSeeds(seeds []CohortSeed, initialPCI float64) []forecast.Cohort {
	if len(seeds) == 0 {
		return nil
	}
	raw := make([]forecast.RawCohort, len(seeds))
	for i, s := range seeds {
		raw[i] = forecast.RawCohort{
			Classification: s.Classification,
			Area:           s.Area,
			DecayRate:      s.DecayRate,
			// The blend prices each resource at its own rate, as its
			// forecast.json entry does (NewParamsForResource).
			CostScale: forecast.ResourceCostScale(s.ResourceType),
		}
	}
	// SpreadCohorts is the same constructor the WASM custom line uses
	// (bridge.Translate), so the static and custom lines agree at equal settings.
	return forecast.SpreadCohorts(raw, initialPCI)
}

// scenariosFromSeeds builds a scenario set from the multi-cohort seeds shared
// with the interactive line. Returns nil when there are no seeds so the caller
// can fall back to the single-synthetic-cohort aggregate shortcut.
func scenariosFromSeeds(seeds []CohortSeed, initialPCI float64, years int, params *forecast.Params) []forecast.ScenarioResult {
	cohorts := cohortsFromSeeds(seeds, initialPCI)
	if cohorts == nil {
		return nil
	}
	return BuildScenarios(cohorts, years, params)
}

// singleCohortScenarios builds a scenario set from one synthetic cohort —
// the aggregate-area fallback used when no per-class cohort stats exist yet.
func singleCohortScenarios(classification string, area, initialPCI, decayRate float64, years int, params *forecast.Params) []forecast.ScenarioResult {
	return BuildScenarios(forecast.SpreadCohorts([]forecast.RawCohort{{
		Classification: classification,
		Area:           area,
		DecayRate:      decayRate,
	}}, initialPCI), years, params)
}

// Emission-time rounding for forecast.json and scenarios.json.
//
// These two files carry full float64 magnitudes — a dollar figure serializes as
// 704.5835576057434 when the site renders it as "$705" — and together they are a
// meaningful share of the exported tree.
//
// Rounding happens HERE, at emission, and on a COPY. It deliberately does not
// happen inside BuildForecastsForCity / BuildScenariosData:
//
//   - BuildHexCostSummary consumes the []ForecastExport those builders return
//     (export.go and handlers.go both) and reads Baseline.Years[0].AnnualNeed,
//     so rounding in the builder would feed it rounded input.
//   - The server memoizes that same slice behind two cached handlers, so
//     rounding it in place would mutate shared state.
//   - It must not move into internal/forecast: Simulate's output is the
//     model's full-precision result, and this is a display concern.
//
// Precisions are chosen to be invisible to the client, not merely "close":
// dollars and areas render with 0-1 decimals against magnitudes in the millions,
// PCI renders at 0 decimals (forecast.EmittedPCI, forecast.ShownPCI), and
// decay rates at 4. Each rounding below keeps at
// least two digits of headroom past what any consumer displays.
const (
	// emitRateDecimals: client renders decay_rate at 4 decimals.
	emitRateDecimals = 6
	// emitRatioDecimals: funding_gap is a ratio the client renders as a percentage.
	emitRatioDecimals = 6
	// emitMaterialDecimals: tonnes and barrels, rendered as whole numbers.
	emitMaterialDecimals = 3
)

// roundMoney rounds to whole currency units. Every dollar figure on the site is
// formatted with maximumFractionDigits: 0, so this is display-identical.
func roundMoney(v float64) float64 { return geo.RoundTo(v, 0) }

// roundArea rounds to whole square metres. Areas are displayed in acres or km2
// to one decimal (1 acre ~ 4047 m2), so 1 m2 granularity is far below anything
// visible, and area_pct is self-normalising over the same rounded values.
func roundArea(v float64) float64 { return geo.RoundTo(v, 0) }

// roundMaterials rounds masses to the kilogram and oil to a hundredth of a
// barrel; the site shows whole tonnes and whole barrels.
func roundMaterials(m forecast.MaterialQuantities) forecast.MaterialQuantities {
	m.MixTonnes = geo.RoundTo(m.MixTonnes, emitMaterialDecimals)
	m.BinderTonnes = geo.RoundTo(m.BinderTonnes, emitMaterialDecimals)
	m.OilBarrels = geo.RoundTo(m.OilBarrels, emitMaterialDecimals)
	return m
}

func roundScenarioResult(r forecast.ScenarioResult) forecast.ScenarioResult {
	r.Scenario.AnnualBudget = roundMoney(r.Scenario.AnnualBudget)
	years := make([]forecast.ScenarioYear, len(r.Years))
	for i, y := range r.Years {
		y.PCI = forecast.EmittedPCI(y.PCI)
		y.Area = roundArea(y.Area)
		y.AnnualNeed = roundMoney(y.AnnualNeed)
		y.AnnualSpend = roundMoney(y.AnnualSpend)
		y.DeferredBacklog = roundMoney(y.DeferredBacklog)
		if y.Materials != nil {
			m := roundMaterials(*y.Materials)
			y.Materials = &m
		}
		if y.MaterialsByTier != nil {
			byTier := make([]forecast.TierMix, len(y.MaterialsByTier))
			for k, tm := range y.MaterialsByTier {
				tm.MixTonnes = geo.RoundTo(tm.MixTonnes, emitMaterialDecimals)
				byTier[k] = tm
			}
			y.MaterialsByTier = byTier
		}
		years[i] = y
	}
	r.Years = years

	cohorts := make([]forecast.CohortSummary, len(r.FinalCohorts))
	for i, c := range r.FinalCohorts {
		c.EndPCI = forecast.EmittedPCI(c.EndPCI)
		c.Area = roundArea(c.Area)
		c.DecayRate = geo.RoundTo(c.DecayRate, emitRateDecimals)
		c.TotalSpend = roundMoney(c.TotalSpend)
		c.DeferredBacklog = roundMoney(c.DeferredBacklog)
		cohorts[i] = c
	}
	r.FinalCohorts = cohorts
	return r
}

func roundScenarioResults(rs []forecast.ScenarioResult) []forecast.ScenarioResult {
	if rs == nil {
		return nil
	}
	out := make([]forecast.ScenarioResult, len(rs))
	for i, r := range rs {
		out[i] = roundScenarioResult(r)
	}
	return out
}

// RoundForecastsForEmission returns a rounded copy of the forecast exports for
// serialization. The input is left untouched, so callers that also derive from
// it (BuildHexCostSummary) keep full precision. TippingPointYear is an int and
// BreakEvenBudget / CurrentBudget / FundingGap are computed inside the builder
// from unrounded years, so rounding them here only affects what is written.
func RoundForecastsForEmission(fes []ForecastExport) []ForecastExport {
	if fes == nil {
		return nil
	}
	out := make([]ForecastExport, len(fes))
	for i, fe := range fes {
		fe.Baseline = roundScenarioResult(fe.Baseline)
		if fe.CityBaseline != nil {
			cb := roundScenarioResult(*fe.CityBaseline)
			fe.CityBaseline = &cb
		}
		fe.Scenarios = roundScenarioResults(fe.Scenarios)
		fe.BreakEvenBudget = roundMoney(fe.BreakEvenBudget)
		fe.RoadsArea = roundArea(fe.RoadsArea)
		fe.CurrentBudget = roundMoney(fe.CurrentBudget)
		if fe.FundingGap != nil {
			g := geo.RoundTo(*fe.FundingGap, emitRatioDecimals)
			fe.FundingGap = &g
		}
		out[i] = fe
	}
	return out
}

// RoundScenariosForEmission returns a shallow copy of the scenarios payload with
// the per-scope scenario slices rounded. Any other key passes through
// unchanged.
func RoundScenariosForEmission(data map[string]any) map[string]any {
	if data == nil {
		return nil
	}
	out := make(map[string]any, len(data))
	for k, v := range data {
		if rs, ok := v.([]forecast.ScenarioResult); ok {
			out[k] = roundScenarioResults(rs)
			continue
		}
		out[k] = v
	}
	return out
}

// BuildHexCostSummary builds the per-scope, per-resource hex cost summary
// from forecast results. The outer key is the scope ("city" or "all"), the
// inner key is the resource type. The "city" sub-map is omitted entirely
// when no ":city" compute rows exist — matching the toggle-visibility gate
// used by the client. It reads nothing: the totals come with each forecast,
// from the same read BuildForecastsForCity built it from.
func BuildHexCostSummary(forecasts []ForecastExport) map[string]map[string]map[string]float64 {
	out := map[string]map[string]map[string]float64{
		"all": {},
	}
	for _, fe := range forecasts {
		cityYear1, allYear1, hasCity := scopeYear1Needs(fe)
		t := resource.Type(fe.ResourceType)
		if resource.ByType(t) == nil {
			continue
		}
		addScopeRow(out, "all", t, allYear1, fe.totalArea)
		if hasCity && fe.hasCityTotal {
			addScopeRow(out, "city", t, cityYear1, fe.cityTotalArea)
		}
	}
	if len(out["all"]) == 0 {
		delete(out, "all")
	}
	return out
}

// scopeYear1Needs reads the year-1 annual need for each scope out of a
// ForecastExport: Baseline's for all, CityBaseline's (when present) for city.
func scopeYear1Needs(fe ForecastExport) (city, all float64, hasCity bool) {
	if len(fe.Baseline.Years) > 0 {
		all = fe.Baseline.Years[0].AnnualNeed
	}
	if fe.CityBaseline == nil {
		return 0, all, false
	}
	if len(fe.CityBaseline.Years) > 0 {
		city = fe.CityBaseline.Years[0].AnnualNeed
	}
	return city, all, true
}

// addScopeRow writes the {year1_need, area} pair under
// out[scope][t.Bare()].
func addScopeRow(out map[string]map[string]map[string]float64, scope string, t resource.Type, year1Need, totalArea float64) {
	if out[scope] == nil {
		out[scope] = make(map[string]map[string]float64)
	}
	out[scope][string(t.Bare())] = map[string]float64{
		"year1_need": year1Need,
		"area":       totalArea,
	}
}

// BuildScenarios generates scenario results for a given area.
func BuildScenarios(cohorts []forecast.Cohort, years int, params *forecast.Params) []forecast.ScenarioResult {
	return forecast.CollapseAllBands(forecast.ReferenceScenarios(cohorts, years, params))
}
