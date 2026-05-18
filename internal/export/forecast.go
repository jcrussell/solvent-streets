package export

import (
	"context"
	"database/sql"
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
func BuildCohortsForResource(ctx context.Context, rt resource.Source, areaSqM float64, store db.Store, fc *config.ForecastConfig) ([]forecast.Cohort, error) {
	currentPCI := fc.InitialPCI
	t := rt.Type()
	stats, err := store.ListCohortStats(ctx, t)
	if err != nil {
		return nil, fmt.Errorf("listing cohort stats for %s: %w", t, err)
	}
	var inputs []forecast.CohortInput
	for _, st := range stats {
		inputs = append(inputs, forecast.CohortInput{
			Classification: st.Classification,
			AreaSqM:        st.AreaSqM,
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
			AreaSqM:        areaSqM,
			DecayRate:      defaultRate,
			InitialPCI:     currentPCI,
		}}
	}
	return cohorts, nil
}

// ForecastExport holds per-resource forecast results.
type ForecastExport struct {
	ResourceType string                    `json:"resource_type"`
	Baseline     forecast.ScenarioResult   `json:"baseline"`
	BboxBaseline *forecast.ScenarioResult  `json:"bbox_baseline,omitempty"` // full-bbox scope (shown when "All Roads" is toggled)
	Scenarios    []forecast.ScenarioResult `json:"scenarios"`
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
	var forecasts []ForecastExport
	var errs []error

	for _, rt := range resource.All {
		fe, err := buildResourceForecast(ctx, rt, entry, fc, costTiers, doNothing)
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

// buildResourceForecast builds the forecast for a single resource. Returns
// errSkipResource when the resource has no compute run yet — a legitimate
// skip on a fresh DB. Any other non-nil error is a real DB failure that
// should aggregate up to the caller and trigger cache eviction.
func buildResourceForecast(ctx context.Context, rt resource.Source, entry CityEntry, fc *config.ForecastConfig, costTiers []forecast.CostTier, doNothing forecast.Scenario) (ForecastExport, error) {
	t := rt.Type()
	tName := string(t)
	result, err := entry.Store.LatestComputeResult(ctx, t)
	if errors.Is(err, sql.ErrNoRows) {
		return ForecastExport{}, errSkipResource
	}
	if err != nil {
		return ForecastExport{}, fmt.Errorf("loading compute result for %s: %w", t, err)
	}

	years := fc.Years
	rtParams := forecast.NewParamsForResource(tName, fc.GrowthRate, costTiers)

	bboxCohorts, err := BuildCohortsForResource(ctx, rt, result.TotalAreaSqM, entry.Store, fc)
	if err != nil {
		return ForecastExport{}, err
	}

	// Try city-scoped cohorts — use as primary if available. Empty result is
	// legitimate (not all cities have city-scope data); only a real DB error
	// surfaces.
	cityStats, err := entry.Store.ListCohortStats(ctx, t.With(resource.ScopeCity))
	if err != nil {
		return ForecastExport{}, fmt.Errorf("listing city cohort stats for %s: %w", t, err)
	}
	primaryCohorts, hasCityScope := cityScopeCohorts(cityStats, fc)
	if !hasCityScope {
		primaryCohorts = bboxCohorts
	}

	baseline := forecast.Simulate(doNothing, primaryCohorts, years, rtParams.Cost, rtParams.Growth)
	year1Need := baseline.Years[0].AnnualNeed
	scenarios := forecast.SimulateDefaults(year1Need, primaryCohorts, years, rtParams.Cost, rtParams.Growth)

	fe := ForecastExport{
		ResourceType: tName,
		Baseline:     baseline,
		Scenarios:    scenarios,
	}
	if hasCityScope {
		bboxBaseline := forecast.Simulate(doNothing, bboxCohorts, years, rtParams.Cost, rtParams.Growth)
		fe.BboxBaseline = &bboxBaseline
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
			AreaSqM:        st.AreaSqM,
		})
	}
	cohorts := forecast.BuildCohorts(cityInputs, fc.InitialPCI, fc.DecayRate)
	if cohorts == nil {
		return nil, false
	}
	return cohorts, true
}

// BuildScenariosData builds the aggregate scenarios JSON structure for a city.
// Prefers city-scoped data as the primary ("all") output since that matches
// what a city budget covers. Full-bbox data is available as "bbox".
func BuildScenariosData(ctx context.Context, entry CityEntry, fc *config.ForecastConfig) map[string]any {
	costTiers := ConvertCostTiers(fc)
	params := forecast.NewParams(fc.GrowthRate, costTiers)
	years := fc.Years

	var totalAreaSqM, cityAreaSqM float64
	var cityFeatureCount, allFeatureCount int

	for _, rt := range resource.All {
		t := rt.Type()
		result, err := entry.Store.LatestComputeResult(ctx, t)
		if err != nil || result == nil {
			continue
		}
		totalAreaSqM += result.TotalAreaSqM
		allFeatureCount += result.FeatureCount

		cityResult, err := entry.Store.LatestComputeResult(ctx, t.With(resource.ScopeCity))
		if err == nil && cityResult != nil {
			cityAreaSqM += cityResult.TotalAreaSqM
			cityFeatureCount += cityResult.FeatureCount
		}
	}

	currentPCI := fc.InitialPCI
	defaultRate := forecast.DefaultDecayRates["default"]
	if fc.DecayRate > 0 {
		defaultRate = fc.DecayRate
	}

	bboxCohorts := []forecast.Cohort{{
		Classification: "all",
		AreaSqM:        totalAreaSqM,
		DecayRate:      defaultRate,
		InitialPCI:     currentPCI,
	}}
	bboxScenarios := BuildScenarios(bboxCohorts, years, params)

	// Use city-scoped data as primary when available
	primaryScenarios := bboxScenarios
	if cityAreaSqM > 0 {
		cityCohorts := []forecast.Cohort{{
			Classification: "city",
			AreaSqM:        cityAreaSqM,
			DecayRate:      defaultRate,
			InitialPCI:     currentPCI,
		}}
		primaryScenarios = BuildScenarios(cityCohorts, years, params)
	}

	summary := map[string]any{
		"city_count":    cityFeatureCount,
		"all_count":     allFeatureCount,
		"state_count":   0,
		"county_count":  0,
		"federal_count": 0,
	}
	if totalAreaSqM > 0 && cityAreaSqM > 0 {
		summary["city_pct"] = cityAreaSqM / totalAreaSqM
	}

	out := map[string]any{
		"summary": summary,
	}
	if cityAreaSqM > 0 {
		out["city"] = primaryScenarios
		out["bbox"] = bboxScenarios
	} else {
		out["bbox"] = primaryScenarios
	}

	return out
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
		addScopeRow(ctx, entry, out, "bbox", t, fe.ResourceType, bboxYear1)
		if hasCity {
			addScopeRow(ctx, entry, out, "city", t.With(resource.ScopeCity), fe.ResourceType, cityYear1)
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
// {year1_cost, total_area_sqm} pair under out[scope][resourceType]. Missing
// compute rows are skipped silently — same as the pre-rename behavior.
func addScopeRow(ctx context.Context, entry CityEntry, out map[string]map[string]map[string]float64, scope string, rt resource.Type, resourceType string, year1Cost float64) {
	r, err := entry.Store.LatestComputeResult(ctx, rt)
	if err != nil || r == nil {
		return
	}
	if out[scope] == nil {
		out[scope] = make(map[string]map[string]float64)
	}
	out[scope][resourceType] = map[string]float64{
		"year1_cost":     year1Cost,
		"total_area_sqm": r.TotalAreaSqM,
	}
}

// BuildScenarios generates scenario results for a given area.
func BuildScenarios(cohorts []forecast.Cohort, years int, params *forecast.Params) []forecast.ScenarioResult {
	baseline := forecast.Simulate(
		forecast.Scenario{Name: "baseline", Label: "Baseline (Do Nothing)", Strategy: forecast.StrategyDoNothing},
		cohorts, years, params.Cost, params.Growth,
	)

	year1Need := baseline.Years[0].AnnualNeed
	scenarios := forecast.SimulateDefaults(year1Need, cohorts, years,
		params.Cost, params.Growth)
	return append([]forecast.ScenarioResult{baseline}, scenarios...)
}
