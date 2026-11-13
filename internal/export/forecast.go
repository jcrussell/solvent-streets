package export

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/forecast"
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

// BuildCohortsForResource builds forecast cohorts for a resource type from the store.
// Falls back to a single cohort if no cohort split exists. A non-nil error is
// a real DB failure, not "no rows" — results.LoadCohorts returns an empty
// slice with nil error when there are no matching rows.
func BuildCohortsForResource(ctx context.Context, rt resource.Source, area float64, store db.Store, fc *config.ForecastConfig) ([]forecast.Cohort, error) {
	t := rt.Type()
	stats, err := results.LoadCohorts(ctx, store, t)
	if err != nil {
		return nil, fmt.Errorf("listing cohort stats for %s: %w", t, err)
	}
	return buildCohortsFromStats(t, area, stats, fc), nil
}

// buildCohortsFromStats is the pure shaping kernel of
// BuildCohortsForResource — takes pre-fetched stats instead of a Store
// so callers that batched the DB lookup don't refetch per resource type.
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
type ForecastExport struct {
	ResourceType string                    `json:"resource_type"`
	Baseline     forecast.ScenarioResult   `json:"baseline"`
	AllBaseline  *forecast.ScenarioResult  `json:"all_baseline,omitempty"` // all-jurisdiction scope (shown when the "All" scope is toggled)
	Scenarios    []forecast.ScenarioResult `json:"scenarios"`

	// Solvency metrics — populated for the roads resource only (a city's
	// paving budget is a street budget, so the headline is about streets).
	// BreakEvenBudget is always computed for roads;
	// InsolvencyYear and FundingGap require a configured current_budget and
	// are omitted otherwise. See buildResourceForecast and the methodology doc.
	InsolvencyYear  *int     `json:"insolvency_year,omitempty"`   // omitted = solvent through the horizon, or no budget configured
	BreakEvenBudget float64  `json:"break_even_budget,omitempty"` // smallest constant annual budget that holds the network steady
	CurrentBudget   float64  `json:"current_budget,omitempty"`    // the configured budget the metrics are measured against; omitted when unset
	FundingGap      *float64 `json:"funding_gap,omitempty"`       // (break_even_budget - current_budget)/current_budget; omitted when no budget is configured
}

// errSkipResource sentinel: a resource has no compute run yet (or returned
// a nil row), and the caller should silently skip it rather than treat it
// as a real failure. Distinguishes "legitimate empty for this resource"
// from "real DB error" in the buildResourceForecast tristate.
var errSkipResource = errors.New("no compute result for resource")

// BuildForecastsForCity builds per-resource forecast exports for a city.
// Prefers city-scoped data (excluding state/federal roads) as the primary
// baseline since that matches what a city budget covers. The all-jurisdiction
// baseline is stored as AllBaseline for the "All Roads" toggle.
//
// Returns (forecasts, nil) on success, (nil, err) on any real DB failure.
// A resource with no city totals is treated as "no compute run yet for this
// resource" and silently skipped — that's a legitimate empty state
// for a freshly-bootstrapped city. Real errors across resources are
// aggregated via errors.Join so callers (e.g. server cache thunks) get the
// full picture in one return; any non-nil error discards the partial slice
// so the cache evicts and retries instead of memoizing a partial result.
func BuildForecastsForCity(ctx context.Context, entry CityEntry, fc *config.ForecastConfig, costTiers []forecast.CostTier) ([]ForecastExport, error) {
	doNothing := forecast.Scenario{Name: "baseline", Label: "Baseline (Do Nothing)", Strategy: forecast.StrategyDoNothing}

	// One batched DB roundtrip reads the totals and cohort splits of the
	// 3-resource × 2-scope cross-product, per city.
	allTypes := make([]resource.Type, 0, len(resource.All))
	cityTypes := make([]resource.Type, 0, len(resource.All))
	for _, rt := range resource.All {
		allTypes = append(allTypes, rt.Type())
		cityTypes = append(cityTypes, rt.Type().With(resource.ScopeCity))
	}
	latestByType, cohortStats, err := results.Load(ctx, entry.Store, append(append([]resource.Type{}, allTypes...), cityTypes...))
	if err != nil {
		return nil, fmt.Errorf("loading compute results: %w", err)
	}

	var forecasts []ForecastExport
	var errs []error
	for _, rt := range resource.All {
		fe, err := buildResourceForecast(rt, fc, costTiers, doNothing, latestByType, cohortStats)
		if errors.Is(err, errSkipResource) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
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
func buildResourceForecast(rt resource.Source, fc *config.ForecastConfig, costTiers []forecast.CostTier, doNothing forecast.Scenario, latestByType map[resource.Type]results.Totals, cohortStats map[resource.Type][]results.Cohort) (ForecastExport, error) {
	t := rt.Type()
	tName := string(t)
	result, ok := latestByType[t]
	if !ok {
		return ForecastExport{}, errSkipResource
	}

	years := fc.Years
	rtParams := forecast.NewParamsForResource(tName, fc.GrowthRate, costTiers, fc.TreatmentCycleYears, fc.ResolvedCostOverhead())

	allCohorts := buildCohortsFromStats(t, result.Area, cohortStats[t], fc)

	// Try city-scoped cohorts — use as primary if available. Empty result is
	// legitimate (not all cities have city-scope data).
	primaryCohorts, hasCityScope := cityScopeCohorts(cohortStats[t.With(resource.ScopeCity)], fc)
	if !hasCityScope {
		primaryCohorts = allCohorts
	}

	baseline := forecast.Simulate(doNothing, primaryCohorts, years, rtParams)
	year1Need := baseline.Years[0].AnnualNeed
	scenarios := forecast.SimulateDefaults(year1Need, primaryCohorts, years, rtParams)

	baseline = forecast.CollapseBands(baseline)
	scenarios = forecast.CollapseAllBands(scenarios)

	fe := ForecastExport{
		ResourceType: tName,
		Baseline:     baseline,
		Scenarios:    scenarios,
	}
	if hasCityScope {
		allBaseline := forecast.CollapseBands(forecast.Simulate(doNothing, allCohorts, years, rtParams))
		fe.AllBaseline = &allBaseline
	}

	// Roads-only solvency metrics. Computed on primaryCohorts (city-scope when
	// available — what a city budget covers). Break-even is budget-independent;
	// insolvency year and funding gap need a configured current_budget.
	if t == resource.TypeRoads {
		fe.BreakEvenBudget = forecast.BreakEvenBudget(primaryCohorts, years, rtParams, forecast.StrategyWorstFirst)
		if fc.CurrentBudget > 0 {
			fe.CurrentBudget = fc.CurrentBudget
			currentRun := forecast.Simulate(
				forecast.Scenario{Name: "current-budget", Label: "Current Budget", AnnualBudget: fc.CurrentBudget, Strategy: forecast.StrategyWorstFirst},
				primaryCohorts, years, rtParams,
			)
			if yr, ok := forecast.InsolvencyYear(currentRun, rtParams.CycleYears); ok {
				fe.InsolvencyYear = &yr
			}
			gap := forecast.FundingGap(fe.BreakEvenBudget, fc.CurrentBudget)
			fe.FundingGap = &gap
		}
	}
	return fe, nil
}

// cityScopeCohorts converts city-scope cohort stats to forecast cohorts.
// Returns (nil, false) if the stats are empty or BuildCohorts rejects them,
// signalling the caller should fall back to all-scope cohorts.
func cityScopeCohorts(cityStats []results.Cohort, fc *config.ForecastConfig) ([]forecast.Cohort, bool) {
	cohorts := forecast.CohortsFromInputs(cohortInputs(cityStats), fc.InitialPCI, fc.DecayRate)
	return cohorts, cohorts != nil
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
	params := forecast.NewParams(fc.GrowthRate, costTiers, fc.TreatmentCycleYears, fc.ResolvedCostOverhead())
	defaultRate := forecast.ResolveDecayRate(fc.DecayRate)

	areas, err := aggregateScenarioAreas(ctx, entry)
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
	allScenarios := scenariosFromSeeds(allSeeds, fc.InitialPCI, fc.Years, params)
	if allScenarios == nil {
		allScenarios = singleCohortScenarios("all", areas.allArea, fc.InitialPCI, defaultRate, fc.Years, params)
	}

	primaryScenarios := allScenarios
	if areas.cityArea > 0 {
		primaryScenarios = scenariosFromSeeds(citySeeds, fc.InitialPCI, fc.Years, params)
		if primaryScenarios == nil {
			primaryScenarios = singleCohortScenarios("city", areas.cityArea, fc.InitialPCI, defaultRate, fc.Years, params)
		}
	}

	out := map[string]any{}
	if areas.cityArea > 0 {
		out["city"] = primaryScenarios
		out["all"] = allScenarios
	} else {
		out["all"] = primaryScenarios
	}
	return out, nil
}

// scenarioAreas pairs all-scope and city-scope aggregate areas across all
// resources for a single CityEntry.
type scenarioAreas struct {
	allArea, cityArea float64
}

// aggregateScenarioAreas sums the paved area across all
// resources for both all and city scopes. The city-scope lookup is gated
// on all-scope row existence for the same resource — a resource with no
// all-scope row contributes to neither total. One batched DB call collects
// both scopes.
func aggregateScenarioAreas(ctx context.Context, entry CityEntry) (scenarioAreas, error) {
	types := make([]resource.Type, 0, 2*len(resource.All))
	for _, rt := range resource.All {
		t := rt.Type()
		types = append(types, t, t.With(resource.ScopeCity))
	}
	latestByType, err := results.LoadTotals(ctx, entry.Store, types)
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
// single config InitialPCI to every cohort (see cmd/wasm/forecast/main.go,
// which sets Cohort.InitialPCI = input.InitialPCI for each seed). Mirror that
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
//   - It must not move into internal/forecast: parity_test.go is there to catch
//     Simulate semantic changes, and this is a display concern.
//
// Precisions are chosen to be invisible to the client, not merely "close":
// dollars and areas render with 0-1 decimals against magnitudes in the millions,
// PCI renders at 0 decimals, and decay rates at 4. Each rounding below keeps at
// least two digits of headroom past what any consumer displays.
const (
	// emitPCIDecimals: client renders PCI at 0 decimals (cohort table) and plots
	// it on a 0-100 axis.
	emitPCIDecimals = 2
	// emitRateDecimals: client renders decay_rate at 4 decimals.
	emitRateDecimals = 6
	// emitRatioDecimals: funding_gap is a ratio the client renders as a percentage.
	emitRatioDecimals = 6
)

func roundTo(v float64, decimals int) float64 {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	p := math.Pow(10, float64(decimals))
	return math.Round(v*p) / p
}

// roundMoney rounds to whole currency units. Every dollar figure on the site is
// formatted with maximumFractionDigits: 0, so this is display-identical.
func roundMoney(v float64) float64 { return roundTo(v, 0) }

// roundArea rounds to whole square metres. Areas are displayed in acres or km2
// to one decimal (1 acre ~ 4047 m2), so 1 m2 granularity is far below anything
// visible, and area_pct is self-normalising over the same rounded values.
func roundArea(v float64) float64 { return roundTo(v, 0) }

func roundScenarioResult(r forecast.ScenarioResult) forecast.ScenarioResult {
	years := make([]forecast.ScenarioYear, len(r.Years))
	for i, y := range r.Years {
		y.PCI = roundTo(y.PCI, emitPCIDecimals)
		y.Area = roundArea(y.Area)
		y.AnnualNeed = roundMoney(y.AnnualNeed)
		y.AnnualSpend = roundMoney(y.AnnualSpend)
		y.DeferredBacklog = roundMoney(y.DeferredBacklog)
		years[i] = y
	}
	r.Years = years

	cohorts := make([]forecast.CohortSummary, len(r.FinalCohorts))
	for i, c := range r.FinalCohorts {
		c.EndPCI = roundTo(c.EndPCI, emitPCIDecimals)
		c.Area = roundArea(c.Area)
		c.DecayRate = roundTo(c.DecayRate, emitRateDecimals)
		c.TotalSpend = roundMoney(c.TotalSpend)
		c.TotalDeficit = roundMoney(c.TotalDeficit)
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
// it (BuildHexCostSummary) keep full precision. InsolvencyYear is an int and
// BreakEvenBudget / CurrentBudget / FundingGap are computed inside the builder
// from unrounded years, so rounding them here only affects what is written.
func RoundForecastsForEmission(fes []ForecastExport) []ForecastExport {
	if fes == nil {
		return nil
	}
	out := make([]ForecastExport, len(fes))
	for i, fe := range fes {
		fe.Baseline = roundScenarioResult(fe.Baseline)
		if fe.AllBaseline != nil {
			bb := roundScenarioResult(*fe.AllBaseline)
			fe.AllBaseline = &bb
		}
		fe.Scenarios = roundScenarioResults(fe.Scenarios)
		fe.BreakEvenBudget = roundMoney(fe.BreakEvenBudget)
		fe.CurrentBudget = roundMoney(fe.CurrentBudget)
		if fe.FundingGap != nil {
			g := roundTo(*fe.FundingGap, emitRatioDecimals)
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
// used by the client.
func BuildHexCostSummary(ctx context.Context, entry CityEntry, forecasts []ForecastExport) map[string]map[string]map[string]float64 {
	out := map[string]map[string]map[string]float64{
		"all": {},
	}
	for _, fe := range forecasts {
		cityYear1, allYear1, hasCity := scopeYear1Needs(fe)
		t := resource.Type(fe.ResourceType)
		if resource.ByType(t) == nil {
			continue
		}
		addScopeRow(ctx, entry, out, "all", t, allYear1)
		if hasCity {
			addScopeRow(ctx, entry, out, "city", t.With(resource.ScopeCity), cityYear1)
		}
	}
	if len(out["all"]) == 0 {
		delete(out, "all")
	}
	return out
}

// scopeYear1Needs reads the year-1 annual need for each scope out of a
// ForecastExport. The convention is set in BuildForecastForResource: when
// AllBaseline is non-nil, Baseline is the city-scoped run; when nil,
// Baseline carries the all-scope run alone.
func scopeYear1Needs(fe ForecastExport) (city, all float64, hasCity bool) {
	hasCity = fe.AllBaseline != nil
	if hasCity {
		if len(fe.Baseline.Years) > 0 {
			city = fe.Baseline.Years[0].AnnualNeed
		}
		if len(fe.AllBaseline.Years) > 0 {
			all = fe.AllBaseline.Years[0].AnnualNeed
		}
		return city, all, true
	}
	if len(fe.Baseline.Years) > 0 {
		all = fe.Baseline.Years[0].AnnualNeed
	}
	return 0, all, false
}

// addScopeRow looks up the compute row for rt and writes the
// {year1_need, total_area} pair under out[scope][rt.Bare()]. Missing
// compute rows are skipped silently.
func addScopeRow(ctx context.Context, entry CityEntry, out map[string]map[string]map[string]float64, scope string, rt resource.Type, year1Need float64) {
	r, ok, err := results.LoadTotal(ctx, entry.Store, rt)
	if err != nil || !ok {
		return
	}
	if out[scope] == nil {
		out[scope] = make(map[string]map[string]float64)
	}
	out[scope][string(rt.Bare())] = map[string]float64{
		"year1_need": year1Need,
		"total_area": r.Area,
	}
}

// BuildScenarios generates scenario results for a given area.
func BuildScenarios(cohorts []forecast.Cohort, years int, params *forecast.Params) []forecast.ScenarioResult {
	return forecast.CollapseAllBands(forecast.ReferenceScenarios(cohorts, years, params))
}
