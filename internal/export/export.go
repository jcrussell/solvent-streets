package export

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/yuin/goldmark"

	"github.com/peterstace/simplefeatures/geom"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/units"
)

// CityEntry holds the config and store for a single city.
type CityEntry struct {
	Config *config.Config
	City   config.CityConfig
	Store  db.Store
	Slug   string
}

//go:embed templates
var templatesFS embed.FS

// methodologyMarkdown is the prose source of truth for the methodology
// section. It is embedded at compile time from an in-repo file. Goldmark's
// default HTML renderer escapes raw HTML blocks (no html.WithUnsafe), so a
// stray <script> in this file becomes escaped text rather than live markup
// — but the template.HTML wrapper still bypasses Go's escaper on the rendered
// output, so do NOT reuse this pattern for markdown sourced from user input,
// config, or the network without also adding a sanitizer.
//
//go:embed docs/methodology.md
var methodologyMarkdown []byte

// methodologyHTMLOnce lazily renders the embedded methodology markdown.
// Lazy so that programs which import internal/export but never render
// methodology (pvmt serve, pvmt forecast, most tests) don't pay the parse
// cost and don't crash at init time if goldmark ever rejects the source.
var methodologyHTMLOnce = sync.OnceValue(func() template.HTML {
	var buf bytes.Buffer
	if err := goldmark.New().Convert(methodologyMarkdown, &buf); err != nil {
		panic(fmt.Errorf("render methodology markdown: %w", err))
	}
	return template.HTML(buf.String())
})

// MethodologyHTML returns the rendered methodology prose. The source lives
// in internal/export/docs/methodology.md; numeric model parameters (decay
// rates, cost tiers) deliberately do not live there — they remain in the
// forecast package and surface wherever they are actually used.
func MethodologyHTML() template.HTML { return methodologyHTMLOnce() }

//go:embed wasm/forecast.wasm
var forecastWasm []byte

//go:embed wasm/wasm_exec.js
var wasmExecJS []byte

// TemplateFS returns the embedded template filesystem for use by the server.
func TemplateFS() fs.ReadFileFS {
	return templatesFS
}

// ForecastWasm returns the embedded WASM binary for the forecast simulator.
func ForecastWasm() []byte { return forecastWasm }

// WasmExecJS returns the embedded Go WASM support JavaScript.
func WasmExecJS() []byte { return wasmExecJS }

// CityInfo holds city metadata for the frontend city switcher.
type CityInfo struct {
	Slug      string     `json:"slug"`
	Name      string     `json:"name"`
	BBox      [4]float64 `json:"bbox"`
	CenterLon float64    `json:"center_lon"`
	CenterLat float64    `json:"center_lat"`
}

// TemplateData wraps MetaJSON with the forecast seed for the interactive controls.
type TemplateData struct {
	MetaJSON
	ForecastSeed    template.JS
	LayerColors     template.JS // JSON map of resource type → color
	RawTOML         string      // original pvmt.toml contents
	ResolvedTOML    string      // config with all defaults filled in
	UnitSystem      string      // "metric" or "imperial"
	Cities          []CityInfo
	WasmPrefix      string // path prefix for WASM assets (e.g. "../"); empty = same directory
	MethodologyHTML template.HTML
}

// resourceColorsJS is the pre-marshaled JSON of ResourceColors, computed at init time.
var resourceColorsJS template.JS

func init() {
	data, err := json.Marshal(ResourceColors)
	if err != nil {
		// ResourceColors is a constant map[string]string — marshal cannot fail.
		panic(fmt.Sprintf("marshal resource colors: %v", err))
	}
	resourceColorsJS = template.JS(data)
}

// ResourceColorsJS returns ResourceColors as a template.JS JSON object.
func ResourceColorsJS() template.JS {
	return resourceColorsJS
}

type MetaJSON struct {
	ProjectName   string     `json:"project_name"`
	BBox          [4]float64 `json:"bbox"`
	CenterLon     float64    `json:"center_lon"`
	CenterLat     float64    `json:"center_lat"`
	SnapshotDate  string     `json:"snapshot_date"`
	Stats         []StatJSON `json:"stats"`
	CityAreaSqM   float64    `json:"city_area_sqm,omitempty"`
	TotalPavedSqM float64    `json:"total_paved_sqm,omitempty"`
	PctPaved      float64    `json:"pct_paved,omitempty"`
}

type StatJSON struct {
	Type         string  `json:"type"`
	Color        string  `json:"color"`
	TotalAreaSqM float64 `json:"total_area_sqm"`
	FeatureCount int     `json:"feature_count"`
}

// ResourceColors maps resource type names to their display colors.
var ResourceColors = map[string]string{
	"roads":     "#6b7280",
	"parking":   "#3b82f6",
	"sidewalks": "#f59e0b",
}

// BuildCityEntries creates CityEntry values for the given cities.
func BuildCityEntries(ctx context.Context, rootDB db.RootStorer, cfg *config.Config, cities []config.CityConfig) ([]CityEntry, error) {
	var entries []CityEntry
	var errs []string
	for _, city := range cities {
		id, err := rootDB.EnsureCity(ctx, city.Slug(), city.Name)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", city.Name, err))
			continue
		}
		entries = append(entries, CityEntry{
			Config: cfg,
			City:   city,
			Store:  rootDB.ForCity(id),
			Slug:   city.Slug(),
		})
	}
	if len(entries) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("no cities loaded: %s", errs[0])
	}
	return entries, nil
}

// LookupCityEntries creates CityEntry values for cities that already exist in
// the database. Unlike BuildCityEntries it never creates city rows, making it
// safe for read-only tools like site generators.
func LookupCityEntries(ctx context.Context, rootDB db.RootStorer, cfg *config.Config, cities []config.CityConfig) ([]CityEntry, error) {
	var entries []CityEntry
	var errs []string
	for _, city := range cities {
		c, err := rootDB.LookupCity(ctx, city.Slug())
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", city.Name, err))
			continue
		}
		entries = append(entries, CityEntry{
			Config: cfg,
			City:   city,
			Store:  rootDB.ForCity(c.ID),
			Slug:   city.Slug(),
		})
	}
	if len(entries) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("no cities found (run 'pvmt ingest' first): %s", errs[0])
	}
	return entries, nil
}

// --- Shared data-building functions (used by both export and server) ---

// BuildMeta builds metadata JSON for a city entry.
func BuildMeta(ctx context.Context, entry CityEntry) (MetaJSON, error) {
	bbox, lon, lat, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		return MetaJSON{}, fmt.Errorf("city %s: %w", entry.City.Name, err)
	}
	meta := MetaJSON{
		ProjectName:  entry.City.Name,
		BBox:         bbox,
		CenterLon:    lon,
		CenterLat:    lat,
		SnapshotDate: time.Now().Format("2006-01-02"),
	}
	for _, rt := range resource.All {
		result, err := entry.Store.LatestComputeResult(ctx, rt.Name())
		if err != nil {
			continue
		}
		meta.Stats = append(meta.Stats, StatJSON{
			Type:         result.ResourceType,
			Color:        ResourceColors[result.ResourceType],
			TotalAreaSqM: result.TotalAreaSqM,
			FeatureCount: result.FeatureCount,
		})
	}

	// Total paved area across all resource types: prefer the cross-resource
	// union row written by `pvmt all compute` (RunCombined). Fall back to
	// summing per-resource rows when the combined row is missing — the sum
	// inflates pct_paved by the road/sidewalk/parking buffer overlap, but
	// keeps single-resource workflows usable until `all compute` runs.
	meta.TotalPavedSqM = totalPavedFromStore(ctx, entry.Store, meta.Stats)

	// Compute city boundary area and % paved.
	if boundaryGJSON, err := entry.Store.GetBoundary(ctx); err == nil && boundaryGJSON != "" {
		if cityAreaSqM, err := geo.BoundaryAreaSqM(boundaryGJSON); err == nil && cityAreaSqM > 0 {
			meta.CityAreaSqM = cityAreaSqM
			if meta.TotalPavedSqM > 0 {
				meta.PctPaved = meta.TotalPavedSqM / cityAreaSqM * 100
			}
		}
	}

	return meta, nil
}

// totalPavedFromStore returns the cross-resource paved area: the "combined"
// ComputeResult if present, otherwise the sum of per-resource Stats. The
// fallback intentionally double-counts where buffers overlap (the bug that
// motivated RunCombined) — better than reporting zero before `all compute`
// has populated the combined row.
func totalPavedFromStore(ctx context.Context, store db.Store, perResource []StatJSON) float64 {
	if r, err := store.LatestComputeResult(ctx, resource.CombinedAll); err == nil && r != nil {
		return r.TotalAreaSqM
	}
	var sum float64
	for _, st := range perResource {
		sum += st.TotalAreaSqM
	}
	return sum
}

// BuildHexGeoJSON builds a GeoJSON FeatureCollection of hex stats for a city.
func BuildHexGeoJSON(ctx context.Context, entry CityEntry, proj *geo.UTMProjector) map[string]any {
	var allStats []db.HexStat
	for _, rt := range resource.All {
		stats, err := entry.Store.ListHexStats(ctx, rt.Name())
		if err != nil {
			continue
		}
		allStats = append(allStats, stats...)
	}

	if len(allStats) == 0 {
		return nil
	}

	bbox, _, _, _ := entry.BBoxAndCenter(ctx)
	hexEdge := entry.Config.ResolvedHexEdge(&entry.City)
	minX, minY, _ := proj.ToProjected(bbox[1], bbox[0])
	maxX, maxY, _ := proj.ToProjected(bbox[3], bbox[2])
	hexes := geo.HexGrid(minX, minY, maxX, maxY, hexEdge)

	hexes = clipHexGridToBoundary(ctx, hexes, entry, proj)

	hexMap := make(map[string]*geo.Hex, len(hexes))
	for i := range hexes {
		hexMap[hexes[i].ID] = &hexes[i]
	}

	// Drop boundary slivers from the heatmap: a single feature inside a tiny
	// clipped hex would render as 100% coverage and visually misrepresent the
	// edge. Aggregate stats are unaffected — the filter sits here, not in
	// ComputeHexStats, so pct_paved's numerator/denominator scope matches.
	const minHexAreaSqM = 100.0
	var features []map[string]any
	for _, st := range allStats {
		h, ok := hexMap[st.HexID]
		if !ok || h.Geom.Area() < minHexAreaSqM {
			continue
		}
		if feat, ok := buildHexFeature(st, hexMap, proj); ok {
			features = append(features, feat)
		}
	}

	return map[string]any{
		"type":     "FeatureCollection",
		"features": features,
	}
}

func clipHexGridToBoundary(ctx context.Context, hexes []geo.Hex, entry CityEntry, proj *geo.UTMProjector) []geo.Hex {
	boundaryGJSON, err := entry.Store.GetBoundary(ctx)
	if err != nil || boundaryGJSON == "" {
		return hexes
	}
	boundaryGeom, _, gErr := geo.GeoJSONToProjectedGeometry(boundaryGJSON, proj)
	if gErr != nil || boundaryGeom.IsEmpty() {
		return hexes
	}
	filtered := make([]geo.Hex, 0, len(hexes))
	for _, h := range hexes {
		inter, iErr := geom.Intersection(h.Geom, boundaryGeom)
		if iErr == nil && !inter.IsEmpty() {
			h.Geom = inter
			filtered = append(filtered, h)
		}
	}
	return filtered
}

// buildHexFeature builds a single GeoJSON feature from a hex stat entry.
// Returns the feature map and true if successful, or nil and false otherwise.
func buildHexFeature(st db.HexStat, hexMap map[string]*geo.Hex, proj *geo.UTMProjector) (map[string]any, bool) {
	h, ok := hexMap[st.HexID]
	if !ok {
		return nil, false
	}
	gjson, err := geo.GeometryToGeoJSON(h.Geom, proj)
	if err != nil {
		return nil, false
	}
	return map[string]any{
		"type":     "Feature",
		"geometry": json.RawMessage(gjson),
		"properties": map[string]any{
			"hex_id":        st.HexID,
			"resource_type": st.ResourceType,
			"area_sqm":      st.AreaSqM,
			"pct_covered":   st.PctCovered,
		},
	}, true
}

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
func BuildCohortsForResource(ctx context.Context, rt resource.ResourceType, areaSqM float64, store db.Store, fc *config.ForecastConfig) ([]forecast.Cohort, error) {
	currentPCI := fc.InitialPCI
	stats, err := store.ListCohortStats(ctx, rt.Name())
	if err != nil {
		return nil, fmt.Errorf("listing cohort stats for %s: %w", rt.Name(), err)
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
		defaultRate := forecast.DecayRateForClass(rt.Name())
		if fc.DecayRate > 0 && forecast.IsRoadClass(rt.Name()) {
			defaultRate = fc.DecayRate
		}
		cohorts = []forecast.Cohort{{
			Classification: rt.Name(),
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
func buildResourceForecast(ctx context.Context, rt resource.ResourceType, entry CityEntry, fc *config.ForecastConfig, costTiers []forecast.CostTier, doNothing forecast.Scenario) (ForecastExport, error) {
	result, err := entry.Store.LatestComputeResult(ctx, rt.Name())
	if errors.Is(err, sql.ErrNoRows) {
		return ForecastExport{}, errSkipResource
	}
	if err != nil {
		return ForecastExport{}, fmt.Errorf("loading compute result for %s: %w", rt.Name(), err)
	}

	years := fc.Years
	rtParams := forecast.NewParamsForResource(rt.Name(), fc.GrowthRate, costTiers)

	bboxCohorts, err := BuildCohortsForResource(ctx, rt, result.TotalAreaSqM, entry.Store, fc)
	if err != nil {
		return ForecastExport{}, err
	}
	bboxBaseline := forecast.Simulate(doNothing, bboxCohorts, years, rtParams.Cost, rtParams.Growth)

	// Try city-scoped cohorts — use as primary if available. Empty result is
	// legitimate (not all cities have city-scope data); only a real DB error
	// surfaces.
	cityStats, err := entry.Store.ListCohortStats(ctx, rt.Name()+":city")
	if err != nil {
		return ForecastExport{}, fmt.Errorf("listing city cohort stats for %s: %w", rt.Name(), err)
	}
	primaryCohorts, hasCityScope := cityScopeCohorts(cityStats, fc)
	if !hasCityScope {
		primaryCohorts = bboxCohorts
	}

	baseline := forecast.Simulate(doNothing, primaryCohorts, years, rtParams.Cost, rtParams.Growth)
	year1Need := baseline.Years[0].AnnualNeed
	scenarios := forecast.SimulateDefaults(year1Need, primaryCohorts, years, rtParams.Cost, rtParams.Growth)

	fe := ForecastExport{
		ResourceType: rt.Name(),
		Baseline:     baseline,
		Scenarios:    scenarios,
	}
	if hasCityScope {
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
		result, err := entry.Store.LatestComputeResult(ctx, rt.Name())
		if err != nil || result == nil {
			continue
		}
		totalAreaSqM += result.TotalAreaSqM
		allFeatureCount += result.FeatureCount

		cityResult, err := entry.Store.LatestComputeResult(ctx, rt.Name()+":city")
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
		"all":     primaryScenarios,
		"summary": summary,
	}
	if cityAreaSqM > 0 {
		out["bbox"] = bboxScenarios
	}

	return out
}

// BuildHexCostSummary builds the hex cost summary from forecast results.
// Prefers city-scoped area to match the city-scoped baseline costs.
func BuildHexCostSummary(ctx context.Context, entry CityEntry, forecasts []ForecastExport) map[string]map[string]float64 {
	result := make(map[string]map[string]float64)
	for _, fe := range forecasts {
		var year1Cost float64
		if len(fe.Baseline.Years) > 0 {
			year1Cost = fe.Baseline.Years[0].AnnualNeed
		}
		// Prefer city-scoped area to match the baseline scope
		cr, err := entry.Store.LatestComputeResult(ctx, fe.ResourceType+":city")
		if err != nil || cr == nil {
			cr, err = entry.Store.LatestComputeResult(ctx, fe.ResourceType)
			if err != nil || cr == nil {
				continue
			}
		}
		result[fe.ResourceType] = map[string]float64{
			"year1_cost":     year1Cost,
			"total_area_sqm": cr.TotalAreaSqM,
		}
	}
	return result
}

// CohortSeed holds per-cohort data for interactive controls.
type CohortSeed struct {
	Classification string  `json:"classification"`
	AreaSqM        float64 `json:"area_sqm"`
	DecayRate      float64 `json:"decay_rate"`
}

// ForecastSeedJSON holds the data needed by the browser to initialize interactive controls.
// CityPavedSqM is the city-jurisdiction paved area, NOT the city boundary area
// (that's MetaJSON.CityAreaSqM). The fields used to share the json tag
// "city_area_sqm" with 14x divergence; never reintroduce that name here.
type ForecastSeedJSON struct {
	InitialPCI   float64             `json:"initial_pci"`
	DecayRate    float64             `json:"decay_rate"`
	GrowthRate   float64             `json:"growth_rate"`
	Years        int                 `json:"years"`
	TotalAreaSqM float64             `json:"total_area_sqm"`
	CityPavedSqM float64             `json:"city_paved_sqm"`
	CostTiers    []forecast.CostTier `json:"cost_tiers"`
	Cohorts      []CohortSeed        `json:"cohorts,omitempty"`
	CityCohorts  []CohortSeed        `json:"city_cohorts,omitempty"`
}

// BuildForecastSeed constructs a ForecastSeedJSON for the given forecast config and store.
func BuildForecastSeed(ctx context.Context, fc *config.ForecastConfig, store db.Store) template.JS {
	costTiers := ConvertCostTiers(fc)
	if len(costTiers) == 0 {
		costTiers = forecast.DefaultCostTiers
	}

	// Prefer the cross-resource union rows (RunCombined). Fall back to summing
	// per-resource rows when missing — same behavior as BuildMeta.
	var totalArea, cityArea float64
	if r, err := store.LatestComputeResult(ctx, resource.CombinedAll); err == nil && r != nil {
		totalArea = r.TotalAreaSqM
	} else {
		for _, rt := range resource.All {
			result, err := store.LatestComputeResult(ctx, rt.Name())
			if err != nil || result == nil {
				continue
			}
			totalArea += result.TotalAreaSqM
		}
	}
	if r, err := store.LatestComputeResult(ctx, resource.CombinedCity); err == nil && r != nil {
		cityArea = r.TotalAreaSqM
	} else {
		for _, rt := range resource.All {
			cityResult, err := store.LatestComputeResult(ctx, rt.Name()+":city")
			if err == nil && cityResult != nil {
				cityArea += cityResult.TotalAreaSqM
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
		InitialPCI:   fc.InitialPCI,
		DecayRate:    decayRate,
		GrowthRate:   fc.GrowthRate,
		Years:        years,
		TotalAreaSqM: totalArea,
		CityPavedSqM: cityArea,
		CostTiers:    costTiers,
		Cohorts:      cohortSeeds,
		CityCohorts:  cityCohortSeeds,
	}
	data, err := json.Marshal(seed)
	if err != nil {
		// ForecastSeedJSON is built from simple Go types; marshal should not fail.
		panic(fmt.Sprintf("marshal forecast seed: %v", err))
	}
	return template.JS(data)
}

// BuildMultiCityMeta aggregates each sub-city's per-resource compute results
// and unioned boundary into a single regional MetaJSON for the multi-city
// landing page. Per-resource Stats sum across entries; CityAreaSqM is the
// area of the union of sub-city boundaries (projected through one shared
// UTM zone derived from the regional bbox). TotalPavedSqM prefers the
// summed "combined" rows with a fallback to summed per-resource rows.
func BuildMultiCityMeta(ctx context.Context, entries []CityEntry, regionName string) (MetaJSON, error) {
	if len(entries) == 0 {
		return MetaJSON{}, errors.New("no entries to aggregate")
	}
	bbox, hasBBox := regionBBox(ctx, entries)
	if !hasBBox {
		return MetaJSON{}, errors.New("no usable boundaries across entries")
	}
	centerLon, centerLat := geo.CenterFromBBox(bbox)

	meta := MetaJSON{
		ProjectName:  regionName,
		BBox:         bbox,
		CenterLon:    centerLon,
		CenterLat:    centerLat,
		SnapshotDate: time.Now().Format("2006-01-02"),
		Stats:        aggregatePerResourceStats(ctx, entries),
	}
	meta.TotalPavedSqM = aggregateTotalPaved(ctx, entries)
	meta.CityAreaSqM = unionedBoundaryArea(ctx, entries, geo.NewUTMProjector(centerLon, centerLat))
	if meta.CityAreaSqM > 0 && meta.TotalPavedSqM > 0 {
		meta.PctPaved = meta.TotalPavedSqM / meta.CityAreaSqM * 100
	}
	return meta, nil
}

// aggregatePerResourceStats sums TotalAreaSqM and FeatureCount per resource
// type across all entries, returning the per-resource cards in resource.All
// order. Resources with no rows in any entry are omitted.
func aggregatePerResourceStats(ctx context.Context, entries []CityEntry) []StatJSON {
	statByType := make(map[string]*StatJSON)
	for _, entry := range entries {
		for _, rt := range resource.All {
			result, err := entry.Store.LatestComputeResult(ctx, rt.Name())
			if err != nil || result == nil {
				continue
			}
			st, ok := statByType[rt.Name()]
			if !ok {
				st = &StatJSON{Type: rt.Name(), Color: ResourceColors[rt.Name()]}
				statByType[rt.Name()] = st
			}
			st.TotalAreaSqM += result.TotalAreaSqM
			st.FeatureCount += result.FeatureCount
		}
	}
	var out []StatJSON
	for _, rt := range resource.All {
		if st, ok := statByType[rt.Name()]; ok {
			out = append(out, *st)
		}
	}
	return out
}

// aggregateTotalPaved sums each entry's cross-resource paved area: the
// "combined" row when present, otherwise that entry's per-resource sum.
// Falling back per-entry (rather than flipping a global flag) keeps mixed
// rollout states correct: when only some entries have been re-run through
// `pvmt all compute`, the not-yet-computed entries still contribute via
// their per-resource sum instead of dropping out of the regional total.
func aggregateTotalPaved(ctx context.Context, entries []CityEntry) float64 {
	var sum float64
	for _, entry := range entries {
		sum += entryAreaWithFallback(ctx, entry.Store, resource.CombinedAll, "")
	}
	return sum
}

// unionedBoundaryArea projects each sub-city boundary through the shared UTM
// projector and unions them, returning the resulting area in sqm. Returns 0
// when no usable boundaries are available.
func unionedBoundaryArea(ctx context.Context, entries []CityEntry, sharedProj *geo.UTMProjector) float64 {
	var boundaries []geom.Geometry
	for _, entry := range entries {
		gjson, err := entry.Store.GetBoundary(ctx)
		if err != nil || gjson == "" {
			continue
		}
		g, _, err := geo.GeoJSONToProjectedGeometry(gjson, sharedProj)
		if err != nil || g.IsEmpty() {
			continue
		}
		boundaries = append(boundaries, g)
	}
	if len(boundaries) == 0 {
		return 0
	}
	unioned, err := geo.UnionAll(boundaries)
	if err != nil || unioned.IsEmpty() {
		return 0
	}
	return unioned.Area()
}

// regionBBox returns the union of sub-city bboxes ([south, west, north, east]).
func regionBBox(ctx context.Context, entries []CityEntry) ([4]float64, bool) {
	var out [4]float64
	first := true
	for _, entry := range entries {
		bb, _, _, err := entry.BBoxAndCenter(ctx)
		if err != nil {
			continue
		}
		if first {
			out = bb
			first = false
			continue
		}
		if bb[0] < out[0] {
			out[0] = bb[0]
		}
		if bb[1] < out[1] {
			out[1] = bb[1]
		}
		if bb[2] > out[2] {
			out[2] = bb[2]
		}
		if bb[3] > out[3] {
			out[3] = bb[3]
		}
	}
	return out, !first
}

// BuildMultiCityForecastSeed aggregates per-city forecast seeds into one
// regional ForecastSeedJSON. TotalAreaSqM and CityPavedSqM sum the "combined"
// and "combined:city" rows across entries (with per-resource fallback).
// Cohort areas are summed across cities for each (resource_type, classification)
// pair — cross-resource collisions stay as separate cohorts to match the
// single-city collectCohortSeeds shape.
func BuildMultiCityForecastSeed(ctx context.Context, fc *config.ForecastConfig, entries []CityEntry) template.JS {
	costTiers := ConvertCostTiers(fc)
	if len(costTiers) == 0 {
		costTiers = forecast.DefaultCostTiers
	}

	var totalArea, cityArea float64
	for _, entry := range entries {
		totalArea += entryAreaWithFallback(ctx, entry.Store, resource.CombinedAll, "")
		cityArea += entryAreaWithFallback(ctx, entry.Store, resource.CombinedCity, ":city")
	}

	decayRate := fc.DecayRate
	if decayRate <= 0 {
		decayRate = forecast.DefaultDecayRates["default"]
	}
	seed := ForecastSeedJSON{
		InitialPCI:   fc.InitialPCI,
		DecayRate:    decayRate,
		GrowthRate:   fc.GrowthRate,
		Years:        fc.Years,
		TotalAreaSqM: totalArea,
		CityPavedSqM: cityArea,
		CostTiers:    costTiers,
		Cohorts:      mergeCohortSeeds(ctx, entries, fc, false),
		CityCohorts:  mergeCohortSeeds(ctx, entries, fc, true),
	}
	data, err := json.Marshal(seed)
	if err != nil {
		panic(fmt.Sprintf("marshal multi-city forecast seed: %v", err))
	}
	return template.JS(data)
}

// entryAreaWithFallback reads the combined row for a single entry; if absent,
// sums the per-resource rows with the matching suffix ("" for all, ":city"
// for city-jurisdiction).
func entryAreaWithFallback(ctx context.Context, store db.Store, combinedLabel, perResourceSuffix string) float64 {
	if r, err := store.LatestComputeResult(ctx, combinedLabel); err == nil && r != nil {
		return r.TotalAreaSqM
	}
	var sum float64
	for _, rt := range resource.All {
		r, err := store.LatestComputeResult(ctx, rt.Name()+perResourceSuffix)
		if err == nil && r != nil {
			sum += r.TotalAreaSqM
		}
	}
	return sum
}

// mergeCohortSeeds collects cohort stats across entries and sums areas for
// the same (resource_type, classification) pair. cityScope=true reads ":city"
// cohort rows. Cross-resource collisions (e.g. roads "default" vs parking
// "default") stay as separate cohorts — matching collectCohortSeeds' single-
// city shape, where each ListCohortStats(rt) row appears verbatim and a
// classification can recur across resources.
func mergeCohortSeeds(ctx context.Context, entries []CityEntry, fc *config.ForecastConfig, cityScope bool) []CohortSeed {
	type key struct {
		Resource       string
		Classification string
	}
	type bucket struct {
		Order int
		Seed  CohortSeed
	}
	buckets := make(map[key]*bucket)
	nextOrder := 0
	for _, entry := range entries {
		for _, rt := range resource.All {
			label := rt.Name()
			if cityScope {
				label += ":city"
			}
			stats, err := entry.Store.ListCohortStats(ctx, label)
			if err != nil {
				continue
			}
			for _, st := range stats {
				k := key{Resource: rt.Name(), Classification: st.Classification}
				b, ok := buckets[k]
				if !ok {
					b = &bucket{
						Order: nextOrder,
						Seed: CohortSeed{
							Classification: st.Classification,
							DecayRate:      resolvedDecayRate(st.Classification, fc.DecayRate),
						},
					}
					buckets[k] = b
					nextOrder++
				}
				b.Seed.AreaSqM += st.AreaSqM
			}
		}
	}
	if len(buckets) == 0 {
		return nil
	}
	out := make([]CohortSeed, len(buckets))
	for _, b := range buckets {
		out[b.Order] = b.Seed
	}
	return out
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

// collectCohortSeeds iterates over all resource types and collects cohort seed
// data for both the main and city-scoped cohort stats.
func collectCohortSeeds(ctx context.Context, store db.Store, fc *config.ForecastConfig) ([]CohortSeed, []CohortSeed) {
	var cohortSeeds []CohortSeed
	var cityCohortSeeds []CohortSeed
	for _, rt := range resource.All {
		stats, err := store.ListCohortStats(ctx, rt.Name())
		if err == nil {
			for _, st := range stats {
				cohortSeeds = append(cohortSeeds, CohortSeed{
					Classification: st.Classification,
					AreaSqM:        st.AreaSqM,
					DecayRate:      resolvedDecayRate(st.Classification, fc.DecayRate),
				})
			}
		}
		cityStats, err := store.ListCohortStats(ctx, rt.Name()+":city")
		if err == nil {
			for _, st := range cityStats {
				cityCohortSeeds = append(cityCohortSeeds, CohortSeed{
					Classification: st.Classification,
					AreaSqM:        st.AreaSqM,
					DecayRate:      resolvedDecayRate(st.Classification, fc.DecayRate),
				})
			}
		}
	}
	return cohortSeeds, cityCohortSeeds
}

// --- Exporter (static site generation) ---

type Exporter struct {
	entries    []CityEntry
	cfg        *config.Config
	outputDir  string
	unitSystem string
	wasmPrefix string // relative path prefix for WASM assets in generated HTML
	skipWasm   bool   // skip writing WASM files (caller handles shared copy)
}

// validWasmPrefix matches safe relative path prefixes (alphanumeric, dots, slashes, hyphens, underscores).
var validWasmPrefix = regexp.MustCompile(`^[a-zA-Z0-9_./-]*$`)

// SetWasmPrefix sets the relative path prefix for WASM asset references in
// generated HTML (e.g. "../" when WASM is served from a parent directory).
// The prefix must contain only safe path characters.
func (e *Exporter) SetWasmPrefix(prefix string) error {
	if !validWasmPrefix.MatchString(prefix) {
		return fmt.Errorf("invalid WASM prefix %q: must match %s", prefix, validWasmPrefix)
	}
	e.wasmPrefix = prefix
	return nil
}

// SetSkipWasm controls whether the exporter writes WASM files. Set to true
// when the caller writes a single shared copy at a parent directory.
func (e *Exporter) SetSkipWasm(skip bool) { e.skipWasm = skip }

func New(entries []CityEntry, cfg *config.Config, outputDir, unitSystem string) *Exporter {
	return &Exporter{entries: entries, cfg: cfg, outputDir: outputDir, unitSystem: unitSystem}
}

func (e *Exporter) Run(ctx context.Context) error {
	if len(e.entries) == 1 {
		return e.runSingleCity(ctx)
	}
	return e.runMultiCity(ctx)
}

func (e *Exporter) runSingleCity(ctx context.Context) error {
	entry := e.entries[0]
	dataDir := filepath.Join(e.outputDir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	if err := e.exportCityData(ctx, entry, dataDir); err != nil {
		return err
	}

	// Write WASM assets (skip when caller provides a shared copy)
	if !e.skipWasm {
		if err := e.writeWasmAssets(e.outputDir); err != nil {
			return err
		}
	}

	// Read raw TOML and build resolved version for Config tab
	var rawTOML string
	if e.cfg.SourcePath != "" {
		if data, err := os.ReadFile(e.cfg.SourcePath); err == nil {
			rawTOML = string(data)
		}
	}

	fc := e.cfg.ResolvedForecast(&entry.City)
	seed := BuildForecastSeed(ctx, &fc, entry.Store)
	meta, err := BuildMeta(ctx, entry)
	if err != nil {
		return err
	}
	return e.renderHTML(meta, seed, rawTOML, ResolvedTOML(e.cfg), e.unitSystem, nil)
}

func (e *Exporter) runMultiCity(ctx context.Context) error {
	if err := os.MkdirAll(e.outputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	// Export each city
	var cities []CityInfo
	for _, entry := range e.entries {
		cityDataDir := filepath.Join(e.outputDir, "cities", entry.Slug, "data")
		if err := os.MkdirAll(cityDataDir, 0o755); err != nil {
			return fmt.Errorf("create city dir %s: %w", entry.Slug, err)
		}
		if err := e.exportCityData(ctx, entry, cityDataDir); err != nil {
			return fmt.Errorf("export %s: %w", entry.Slug, err)
		}
		info, err := entry.Info(ctx)
		if err != nil {
			return fmt.Errorf("city %s bbox: %w", entry.Slug, err)
		}
		cities = append(cities, info)
	}

	// Write cities.json
	if err := writeJSON(filepath.Join(e.outputDir, "cities.json"), cities); err != nil {
		return fmt.Errorf("write cities.json: %w", err)
	}

	// Write WASM assets (skip when caller provides a shared copy)
	if !e.skipWasm {
		if err := e.writeWasmAssets(e.outputDir); err != nil {
			return err
		}
	}

	// Render the regional landing page: aggregated meta and forecast seed
	// across all sub-cities. Without this aggregation the landing silently
	// presents the first city's totals as the regional headline.
	regionName := e.cfg.Export.Title
	if regionName == "" {
		regionName = filepath.Base(e.outputDir)
	}
	fc := e.cfg.ResolvedForecast(nil)
	meta, err := BuildMultiCityMeta(ctx, e.entries, regionName)
	if err != nil {
		return err
	}
	seed := BuildMultiCityForecastSeed(ctx, &fc, e.entries)

	var rawTOML string
	if e.cfg.SourcePath != "" {
		if data, err := os.ReadFile(e.cfg.SourcePath); err == nil {
			rawTOML = string(data)
		}
	}

	return e.renderHTML(meta, seed, rawTOML, ResolvedTOML(e.cfg), e.unitSystem, cities)
}

func (e *Exporter) exportCityData(ctx context.Context, entry CityEntry, dataDir string) error {
	_, lon, lat, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		return fmt.Errorf("city bbox: %w", err)
	}
	proj := geo.NewUTMProjector(lon, lat)

	meta, err := BuildMeta(ctx, entry)
	if err != nil {
		return err
	}

	// Write boundary.geojson if boundary exists
	if boundaryGJSON, err := entry.Store.GetBoundary(ctx); err == nil && boundaryGJSON != "" {
		fc := map[string]any{
			"type": "FeatureCollection",
			"features": []map[string]any{
				{
					"type":       "Feature",
					"geometry":   json.RawMessage(boundaryGJSON),
					"properties": map[string]any{"type": "boundary"},
				},
			},
		}
		if err := writeJSON(filepath.Join(dataDir, "boundary.geojson"), fc); err != nil {
			return fmt.Errorf("write boundary geojson: %w", err)
		}
	}

	// Export hex grid
	hexFC := BuildHexGeoJSON(ctx, entry, proj)
	if hexFC != nil {
		if err := writeJSON(filepath.Join(dataDir, "hexgrid.geojson"), hexFC); err != nil {
			return fmt.Errorf("write hexgrid: %w", err)
		}
	}

	// Write meta.json
	if err := writeJSON(filepath.Join(dataDir, "meta.json"), meta); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}

	// Export forecast and scenario data
	if err := exportScenariosForCity(ctx, entry, dataDir); err != nil {
		return fmt.Errorf("export scenarios: %w", err)
	}

	// Export forecast seed for interactive WASM controls (per-city)
	forecastCfg := entry.Config.ResolvedForecast(&entry.City)
	seed := BuildForecastSeed(ctx, &forecastCfg, entry.Store)
	if err := os.WriteFile(filepath.Join(dataDir, "forecast_seed.json"), []byte(seed), 0o644); err != nil {
		return fmt.Errorf("write forecast_seed.json: %w", err)
	}

	return nil
}

func exportScenariosForCity(ctx context.Context, entry CityEntry, dataDir string) error {
	fc := entry.Config.ResolvedForecast(&entry.City)
	costTiers := ConvertCostTiers(&fc)

	forecasts, err := BuildForecastsForCity(ctx, entry, &fc, costTiers)
	if err != nil {
		return fmt.Errorf("build forecasts: %w", err)
	}

	if len(forecasts) > 0 {
		if err := writeJSON(filepath.Join(dataDir, "forecast.json"), forecasts); err != nil {
			return fmt.Errorf("write forecast.json: %w", err)
		}

		hexCostSummary := BuildHexCostSummary(ctx, entry, forecasts)
		if err := writeJSON(filepath.Join(dataDir, "hex-cost-summary.json"), hexCostSummary); err != nil {
			return fmt.Errorf("write hex-cost-summary.json: %w", err)
		}

		scenariosOut := BuildScenariosData(ctx, entry, &fc)
		if err := writeJSON(filepath.Join(dataDir, "scenarios.json"), scenariosOut); err != nil {
			return fmt.Errorf("write scenarios.json: %w", err)
		}
	}

	return nil
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

// ResolvedTOML returns the config serialized as TOML with all defaults filled in.
func ResolvedTOML(cfg *config.Config) string {
	resolved := *cfg

	if resolved.Grid.HexEdgeM <= 0 {
		resolved.Grid.HexEdgeM = config.DefaultHexEdgeM
	}
	config.NormalizeForecast(&resolved.Forecast)
	if resolved.Forecast.DecayRate <= 0 {
		resolved.Forecast.DecayRate = forecast.DefaultDecayRates["default"]
	}
	if len(resolved.Forecast.CostTiers) == 0 {
		for _, t := range forecast.DefaultCostTiers {
			resolved.Forecast.CostTiers = append(resolved.Forecast.CostTiers, config.CostTierCfg{
				MinPCI:     t.MinPCI,
				MaxPCI:     t.MaxPCI,
				CostPerSqM: t.CostPerSqM,
				Label:      t.Label,
			})
		}
	}

	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(resolved); err != nil {
		return "# error encoding config"
	}
	return buf.String()
}

func (e *Exporter) writeWasmAssets(dir string) error {
	return WriteSharedWasmAssets(dir)
}

// ExampleInfo describes one example card on the landing page. Defined in
// this package so gensite and the landing-page tests reference the same
// shape; drift between the two used to be invisible until runtime.
type ExampleInfo struct {
	Slug       string
	Title      string
	CityNames  string
	CityCount  int
	HexEdgeM   int
	UnitSystem string
}

// RenderLandingPage writes index.html into outputDir using the embedded
// landing and methodology templates. Centralizing the wiring here means
// both gensite and the internal template tests exercise the same code path.
func RenderLandingPage(outputDir string, examples []ExampleInfo) (err error) {
	landingData, err := templatesFS.ReadFile("templates/landing.html.tmpl")
	if err != nil {
		return fmt.Errorf("read landing template: %w", err)
	}
	methData, err := templatesFS.ReadFile("templates/methodology.html.tmpl")
	if err != nil {
		return fmt.Errorf("read methodology template: %w", err)
	}
	themeData, err := templatesFS.ReadFile("templates/theme.html.tmpl")
	if err != nil {
		return fmt.Errorf("read theme template: %w", err)
	}

	tmpl := template.New("landing")
	if _, err := tmpl.Parse(string(landingData)); err != nil {
		return fmt.Errorf("parse landing template: %w", err)
	}
	if _, err := tmpl.Parse(string(methData)); err != nil {
		return fmt.Errorf("parse methodology template: %w", err)
	}
	if _, err := tmpl.Parse(string(themeData)); err != nil {
		return fmt.Errorf("parse theme template: %w", err)
	}

	f, err := os.Create(filepath.Join(outputDir, "index.html"))
	if err != nil {
		return fmt.Errorf("create index.html: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close index.html: %w", cerr)
		}
	}()

	return tmpl.Execute(f, struct {
		Examples        []ExampleInfo
		MethodologyHTML template.HTML
	}{
		Examples:        examples,
		MethodologyHTML: MethodologyHTML(),
	})
}

// WriteSharedWasmAssets writes the embedded WASM files to dir. Use this when
// writing a single shared copy at a site root instead of per-export copies.
func WriteSharedWasmAssets(dir string) error {
	if err := os.WriteFile(filepath.Join(dir, "forecast.wasm"), forecastWasm, 0o644); err != nil {
		return fmt.Errorf("write forecast.wasm: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wasm_exec.js"), wasmExecJS, 0o644); err != nil {
		return fmt.Errorf("write wasm_exec.js: %w", err)
	}
	return nil
}

func indexFuncMap(sys units.System) template.FuncMap {
	return template.FuncMap{
		"divf":          func(a, b float64) float64 { return a / b },
		"areaLarge":     func(sqm float64) float64 { return units.AreaLargeValue(sqm, sys) },
		"areaVeryLarge": func(sqm float64) float64 { return units.AreaVeryLargeValue(sqm, sys) },
		"areaLargeUnit": func() string {
			if sys == units.Imperial {
				return "acres"
			}
			return "ha"
		},
		"areaVeryLargeUnit": func() string {
			if sys == units.Imperial {
				return "sq mi"
			}
			return "sq km"
		},
	}
}

// ParseIndexTemplate returns the parsed template tree for the index page,
// including the methodology and theme partials that index.html.tmpl references
// via {{template ...}}. Shared between the static exporter and the live server
// so they can't drift.
func ParseIndexTemplate(sys units.System) (*template.Template, error) {
	files := []string{
		"templates/index.html.tmpl",
		"templates/methodology.html.tmpl",
		"templates/theme.html.tmpl",
	}
	var tmpl *template.Template
	for _, name := range files {
		data, err := templatesFS.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		if tmpl == nil {
			tmpl, err = template.New("index").Funcs(indexFuncMap(sys)).Parse(string(data))
		} else {
			_, err = tmpl.Parse(string(data))
		}
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
	}
	return tmpl, nil
}

func (e *Exporter) renderHTML(meta MetaJSON, seed template.JS, rawTOML, resolvedTOML, unitSystem string, cities []CityInfo) (err error) {
	sys := units.ParseSystem(unitSystem)
	tmpl, err := ParseIndexTemplate(sys)
	if err != nil {
		return err
	}

	outPath := filepath.Join(e.outputDir, "index.html")
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create index.html: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close index.html: %w", cerr)
		}
	}()

	td := TemplateData{
		MetaJSON:        meta,
		ForecastSeed:    seed,
		LayerColors:     ResourceColorsJS(),
		RawTOML:         rawTOML,
		ResolvedTOML:    resolvedTOML,
		UnitSystem:      unitSystem,
		Cities:          cities,
		WasmPrefix:      e.wasmPrefix,
		MethodologyHTML: MethodologyHTML(),
	}
	return tmpl.Execute(f, td)
}

// BBoxAndCenter derives bbox and center from the stored boundary polygon.
func (entry CityEntry) BBoxAndCenter(ctx context.Context) ([4]float64, float64, float64, error) {
	boundaryGJSON, err := entry.Store.GetBoundary(ctx)
	if err != nil || boundaryGJSON == "" {
		return [4]float64{}, 0, 0, fmt.Errorf("no boundary stored for %s — run 'pvmt ingest' first", entry.City.Name)
	}
	bbox, err := geo.BBoxFromGeoJSON(boundaryGJSON)
	if err != nil {
		return [4]float64{}, 0, 0, err
	}
	lon, lat := geo.CenterFromBBox(bbox)
	return bbox, lon, lat, nil
}

// Info returns the frontend-facing metadata for this city. Callers decide
// whether to skip or fail when the boundary is missing.
func (entry CityEntry) Info(ctx context.Context) (CityInfo, error) {
	bbox, lon, lat, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		return CityInfo{}, err
	}
	return CityInfo{
		Slug:      entry.Slug,
		Name:      entry.City.Name,
		BBox:      bbox,
		CenterLon: lon,
		CenterLat: lat,
	}, nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
