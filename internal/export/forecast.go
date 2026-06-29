package export

import (
	"context"
	"errors"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/resource"
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
// Falls back to a single cohort if no cohort stats exist. A non-nil error is a
// real DB failure, not "no rows" — ListCohortStats returns an empty slice with
// nil error when there are no matching rows.
func BuildCohortsForResource(ctx context.Context, rt resource.Source, area float64, store db.Store, fc *config.ForecastConfig) ([]forecast.Cohort, error) {
	t := rt.Type()
	stats, err := store.ListCohortStats(ctx, t)
	if err != nil {
		return nil, fmt.Errorf("listing cohort stats for %s: %w", t, err)
	}
	return buildCohortsFromStats(t, area, stats, fc), nil
}

// buildCohortsFromStats is the pure shaping kernel of
// BuildCohortsForResource — takes pre-fetched stats instead of a Store
// so callers that batched the DB lookup don't refetch per resource type.
func buildCohortsFromStats(t resource.Type, area float64, stats []db.CohortStat, fc *config.ForecastConfig) []forecast.Cohort {
	currentPCI := fc.InitialPCI
	var inputs []forecast.CohortInput
	for _, st := range stats {
		inputs = append(inputs, forecast.CohortInput{
			Classification: st.Classification,
			Area:           st.Area,
		})
	}
	cohorts := forecast.BuildCohorts(inputs, currentPCI, fc.DecayRate)
	if cohorts == nil {
		tName := string(t)
		defaultRate := forecast.DecayRateForClass(tName)
		if fc.DecayRate > 0 && forecast.IsRoadClass(tName) {
			defaultRate = fc.DecayRate
		}
		cohorts = []forecast.Cohort{{
			Classification: tName,
			Area:           area,
			DecayRate:      defaultRate,
			InitialPCI:     currentPCI,
		}}
	}
	// Spread the single configured mean PCI into a condition distribution so the
	// cost figures reflect the failed/poor tail (docs/validation.md §4). Applied
	// at this chokepoint so every downstream consumer (baseline, scenarios,
	// break-even, insolvency) sees the same corrected cohorts.
	return forecast.ApplyConditionSpread(cohorts)
}

// collapseFinalCohorts aggregates the per-band sub-cohort summaries produced by
// ApplyConditionSpread back to one row per classification for display. Headline
// math (Years/AnnualNeed/break-even) already sums across the bands and is
// unaffected; this only de-duplicates the FinalCohorts table.
func collapseFinalCohorts(r forecast.ScenarioResult) forecast.ScenarioResult {
	r.FinalCohorts = forecast.AggregateCohortSummariesByClass(r.FinalCohorts)
	return r
}

// ForecastExport holds per-resource forecast results.
type ForecastExport struct {
	ResourceType string                    `json:"resource_type"`
	Baseline     forecast.ScenarioResult   `json:"baseline"`
	BboxBaseline *forecast.ScenarioResult  `json:"bbox_baseline,omitempty"` // full-bbox scope (shown when "All Roads" is toggled)
	Scenarios    []forecast.ScenarioResult `json:"scenarios"`

	// Solvency metrics — populated for the roads resource only (the blended
	// scenarios mis-price sidewalks at road tiers, so an advocacy headline
	// must be roads-only). BreakEvenBudget is always computed for roads;
	// InsolvencyYear and FundingGap require a configured current_budget and
	// are nil otherwise. See buildResourceForecast and the methodology doc.
	InsolvencyYear  *int     `json:"insolvency_year"`             // nil = solvent through horizon, or no budget configured
	BreakEvenBudget float64  `json:"break_even_budget,omitempty"` // smallest constant annual budget that holds the network steady
	CurrentBudget   float64  `json:"current_budget,omitempty"`    // the configured budget the metrics are measured against (0 = unset)
	FundingGap      *float64 `json:"funding_gap"`                 // (break_even - current)/current; nil = no budget configured
}

// errSkipResource sentinel: a resource has no compute run yet (or returned
// a nil row), and the caller should silently skip it rather than treat it
// as a real failure. Distinguishes "legitimate empty for this resource"
// from "real DB error" in the buildResourceForecast tristate.
var errSkipResource = errors.New("no compute result for resource")

// BuildForecastsForCity builds per-resource forecast exports for a city.
// Prefers city-scoped data (excluding state/federal roads) as the primary
// baseline since that matches what a city budget covers. The full-bbox
// baseline is stored as BboxBaseline for the "All Roads" toggle.
//
// Returns (forecasts, nil) on success, (nil, err) on any real DB failure.
// sql.ErrNoRows from LatestComputeResult is treated as "no compute run yet
// for this resource" and silently skipped — that's a legitimate empty state
// for a freshly-bootstrapped city. Real errors across resources are
// aggregated via errors.Join so callers (e.g. server cache thunks) get the
// full picture in one return; any non-nil error discards the partial slice
// so the cache evicts and retries instead of memoizing a partial result.
func BuildForecastsForCity(ctx context.Context, entry CityEntry, fc *config.ForecastConfig, costTiers []forecast.CostTier) ([]ForecastExport, error) {
	doNothing := forecast.Scenario{Name: "baseline", Label: "Baseline (Do Nothing)", Strategy: forecast.StrategyDoNothing}

	// Two batched DB roundtrips replace the previous per-resource loop's
	// LatestComputeResult + (per scope) ListCohortStats calls. Across the
	// 3-resource × 2-scope cross-product that's 6 → 1 for compute results
	// and 6 → 1 for cohort stats, per city.
	bboxTypes := make([]resource.Type, 0, len(resource.All))
	cityTypes := make([]resource.Type, 0, len(resource.All))
	for _, rt := range resource.All {
		bboxTypes = append(bboxTypes, rt.Type())
		cityTypes = append(cityTypes, rt.Type().With(resource.ScopeCity))
	}
	latestByType, err := entry.Store.LatestComputeResults(ctx, bboxTypes)
	if err != nil {
		return nil, fmt.Errorf("loading latest compute results: %w", err)
	}
	cohortStats, err := entry.Store.ListCohortStatsForTypes(ctx, append(append([]resource.Type{}, bboxTypes...), cityTypes...))
	if err != nil {
		return nil, fmt.Errorf("loading cohort stats: %w", err)
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
func buildResourceForecast(rt resource.Source, fc *config.ForecastConfig, costTiers []forecast.CostTier, doNothing forecast.Scenario, latestByType map[resource.Type]*db.ComputeResult, cohortStats map[resource.Type][]db.CohortStat) (ForecastExport, error) {
	t := rt.Type()
	tName := string(t)
	result, ok := latestByType[t]
	if !ok || result == nil {
		return ForecastExport{}, errSkipResource
	}

	years := fc.Years
	rtParams := forecast.NewParamsForResource(tName, fc.GrowthRate, costTiers, fc.TreatmentCycleYears)

	bboxCohorts := buildCohortsFromStats(t, result.TotalArea, cohortStats[t], fc)

	// Try city-scoped cohorts — use as primary if available. Empty result is
	// legitimate (not all cities have city-scope data).
	primaryCohorts, hasCityScope := cityScopeCohorts(cohortStats[t.With(resource.ScopeCity)], fc)
	if !hasCityScope {
		primaryCohorts = bboxCohorts
	}

	baseline := forecast.Simulate(doNothing, primaryCohorts, years, rtParams)
	year1Need := baseline.Years[0].AnnualNeed
	scenarios := forecast.SimulateDefaults(year1Need, primaryCohorts, years, rtParams)

	baseline = collapseFinalCohorts(baseline)
	for i := range scenarios {
		scenarios[i] = collapseFinalCohorts(scenarios[i])
	}

	fe := ForecastExport{
		ResourceType: tName,
		Baseline:     baseline,
		Scenarios:    scenarios,
	}
	if hasCityScope {
		bboxBaseline := collapseFinalCohorts(forecast.Simulate(doNothing, bboxCohorts, years, rtParams))
		fe.BboxBaseline = &bboxBaseline
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
// signalling the caller should fall back to bbox-scope cohorts.
func cityScopeCohorts(cityStats []db.CohortStat, fc *config.ForecastConfig) ([]forecast.Cohort, bool) {
	if len(cityStats) == 0 {
		return nil, false
	}
	cityInputs := make([]forecast.CohortInput, 0, len(cityStats))
	for _, st := range cityStats {
		cityInputs = append(cityInputs, forecast.CohortInput{
			Classification: st.Classification,
			Area:           st.Area,
		})
	}
	cohorts := forecast.BuildCohorts(cityInputs, fc.InitialPCI, fc.DecayRate)
	if cohorts == nil {
		return nil, false
	}
	return forecast.ApplyConditionSpread(cohorts), true
}

// BuildScenariosData builds the aggregate scenarios JSON structure for a city.
// Prefers city-scoped data as the primary ("all") output since that matches
// what a city budget covers. Full-bbox data is available as "bbox".
//
// The static Baseline/25%/50%/Full lines are built from the SAME multi-cohort
// seeds (per-class decay rates) that drive the interactive "Custom Scenario"
// line via WASM Simulate — see collectCohortSeeds and index.html.tmpl's
// getControlValues. Building both from one cohort source keeps the static and
// custom lines in agreement on the same chart at equal settings; the previous
// single-synthetic-cohort path (blended decay over a city-paved area) diverged
// from the interactive line by ~4.7%.
//
// AREA SCOPE: the scenario lines now use the cohort-summed area (sum of
// per-class cohort areas), not the city-paved/bbox aggregate compute area, so
// the static and custom lines share an identical area basis. The summary block
// below still reports the aggregate compute areas/feature counts (city_pct,
// *_count) — those are informational rollups, not the chart's area basis.
func BuildScenariosData(ctx context.Context, entry CityEntry, fc *config.ForecastConfig) map[string]any {
	costTiers := ConvertCostTiers(fc)
	params := forecast.NewParams(fc.GrowthRate, costTiers, fc.TreatmentCycleYears)
	defaultRate := forecast.DefaultDecayRates["default"]
	if fc.DecayRate > 0 {
		defaultRate = fc.DecayRate
	}

	areas := aggregateScenarioAreas(ctx, entry)

	// Same cohort seeds the interactive line uses: bbox-scope and city-scope.
	bboxSeeds, citySeeds := collectCohortSeeds(ctx, entry.Store, fc)

	// bbox scenarios from bbox cohorts; fall back to a single synthetic cohort
	// over the aggregate bbox area when no cohort stats exist (fresh DB).
	bboxScenarios := scenariosFromSeeds(bboxSeeds, fc.InitialPCI, fc.Years, params)
	if bboxScenarios == nil {
		bboxScenarios = singleCohortScenarios("all", areas.bboxArea, fc.InitialPCI, defaultRate, fc.Years, params)
	}

	primaryScenarios := bboxScenarios
	if areas.cityArea > 0 {
		primaryScenarios = scenariosFromSeeds(citySeeds, fc.InitialPCI, fc.Years, params)
		if primaryScenarios == nil {
			primaryScenarios = singleCohortScenarios("city", areas.cityArea, fc.InitialPCI, defaultRate, fc.Years, params)
		}
	}

	summary := map[string]any{
		"city_count":    areas.cityFeatures,
		"all_count":     areas.bboxFeatures,
		"state_count":   0,
		"county_count":  0,
		"federal_count": 0,
	}
	if areas.bboxArea > 0 && areas.cityArea > 0 {
		summary["city_pct"] = areas.cityArea / areas.bboxArea
	}

	out := map[string]any{"summary": summary}
	if areas.cityArea > 0 {
		out["city"] = primaryScenarios
		out["bbox"] = bboxScenarios
	} else {
		out["bbox"] = primaryScenarios
	}
	return out
}

// scenarioAreas pairs bbox-scope and city-scope aggregate areas and feature
// counts across all resources for a single CityEntry.
type scenarioAreas struct {
	bboxArea, cityArea         float64
	bboxFeatures, cityFeatures int
}

// aggregateScenarioAreas sums TotalArea and FeatureCount across all
// resources for both bbox and city scopes. The city-scope lookup is gated
// on bbox-row existence for the same resource — a resource with no bbox
// row contributes to neither total, matching the pre-refactor behavior.
// One batched DB call collects both scopes; previously each resource
// type took two round trips.
func aggregateScenarioAreas(ctx context.Context, entry CityEntry) scenarioAreas {
	types := make([]resource.Type, 0, 2*len(resource.All))
	for _, rt := range resource.All {
		t := rt.Type()
		types = append(types, t, t.With(resource.ScopeCity))
	}
	latestByType, err := entry.Store.LatestComputeResults(ctx, types)
	if err != nil {
		return scenarioAreas{}
	}
	var agg scenarioAreas
	for _, rt := range resource.All {
		t := rt.Type()
		result, ok := latestByType[t]
		if !ok || result == nil {
			continue
		}
		agg.bboxArea += result.TotalArea
		agg.bboxFeatures += result.FeatureCount

		if cityResult, ok := latestByType[t.With(resource.ScopeCity)]; ok && cityResult != nil {
			agg.cityArea += cityResult.TotalArea
			agg.cityFeatures += cityResult.FeatureCount
		}
	}
	return agg
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
	cohorts := make([]forecast.Cohort, len(seeds))
	for i, s := range seeds {
		cohorts[i] = forecast.Cohort{
			Classification: s.Classification,
			Area:           s.Area,
			DecayRate:      s.DecayRate,
			InitialPCI:     initialPCI,
		}
	}
	// Spread to match the static-line cohorts and the WASM custom line (which
	// also spreads in bridge.Translate), so all three agree at equal settings.
	return forecast.ApplyConditionSpread(cohorts)
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
	return BuildScenarios(forecast.ApplyConditionSpread([]forecast.Cohort{{
		Classification: classification,
		Area:           area,
		DecayRate:      decayRate,
		InitialPCI:     initialPCI,
	}}), years, params)
}

// BuildHexCostSummary builds the per-scope, per-resource hex cost summary
// from forecast results. The outer key is the scope ("city" or "bbox"), the
// inner key is the resource type. The "city" sub-map is omitted entirely
// when no ":city" compute rows exist — matching the toggle-visibility gate
// used by the client.
func BuildHexCostSummary(ctx context.Context, entry CityEntry, forecasts []ForecastExport) map[string]map[string]map[string]float64 {
	out := map[string]map[string]map[string]float64{
		"bbox": {},
	}
	for _, fe := range forecasts {
		cityYear1, bboxYear1, hasCity := scopeYear1Costs(fe)
		t := resource.Type(fe.ResourceType)
		if resource.ByType(t) == nil {
			continue
		}
		addScopeRow(ctx, entry, out, "bbox", t, bboxYear1)
		if hasCity {
			addScopeRow(ctx, entry, out, "city", t.With(resource.ScopeCity), cityYear1)
		}
	}
	if len(out["bbox"]) == 0 {
		delete(out, "bbox")
	}
	return out
}

// scopeYear1Costs reads the year-1 annual need for each scope out of a
// ForecastExport. The convention is set in BuildForecastForResource: when
// BboxBaseline is non-nil, Baseline is the city-scoped run; when nil,
// Baseline carries the bbox-scope run alone.
func scopeYear1Costs(fe ForecastExport) (city, bbox float64, hasCity bool) {
	hasCity = fe.BboxBaseline != nil
	if hasCity {
		if len(fe.Baseline.Years) > 0 {
			city = fe.Baseline.Years[0].AnnualNeed
		}
		if len(fe.BboxBaseline.Years) > 0 {
			bbox = fe.BboxBaseline.Years[0].AnnualNeed
		}
		return city, bbox, true
	}
	if len(fe.Baseline.Years) > 0 {
		bbox = fe.Baseline.Years[0].AnnualNeed
	}
	return 0, bbox, false
}

// addScopeRow looks up the compute row for rt and writes the
// {year1_cost, total_area} pair under out[scope][rt.Bare()]. Missing
// compute rows are skipped silently — same as the pre-rename behavior.
func addScopeRow(ctx context.Context, entry CityEntry, out map[string]map[string]map[string]float64, scope string, rt resource.Type, year1Cost float64) {
	r, err := entry.Store.LatestComputeResult(ctx, rt)
	if err != nil || r == nil {
		return
	}
	if out[scope] == nil {
		out[scope] = make(map[string]map[string]float64)
	}
	out[scope][string(rt.Bare())] = map[string]float64{
		"year1_cost": year1Cost,
		"total_area": r.TotalArea,
	}
}

// BuildScenarios generates scenario results for a given area.
func BuildScenarios(cohorts []forecast.Cohort, years int, params *forecast.Params) []forecast.ScenarioResult {
	baseline := forecast.Simulate(
		forecast.Scenario{Name: "baseline", Label: "Baseline (Do Nothing)", Strategy: forecast.StrategyDoNothing},
		cohorts, years, params,
	)

	year1Need := baseline.Years[0].AnnualNeed
	scenarios := forecast.SimulateDefaults(year1Need, cohorts, years, params)
	results := append([]forecast.ScenarioResult{baseline}, scenarios...)
	for i := range results {
		results[i] = collapseFinalCohorts(results[i])
	}
	return results
}
