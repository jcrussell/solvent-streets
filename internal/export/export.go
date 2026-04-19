package export

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
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

	"pvmt/internal/config"
	"pvmt/internal/db"
	"pvmt/internal/forecast"
	"pvmt/internal/geo"
	"pvmt/internal/resource"
	"pvmt/internal/units"
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

	// Aggregate paved area across all resource types.
	var totalPavedSqM float64
	for _, st := range meta.Stats {
		totalPavedSqM += st.TotalAreaSqM
	}
	meta.TotalPavedSqM = totalPavedSqM

	// Compute city boundary area and % paved.
	if boundaryGJSON, err := entry.Store.GetBoundary(ctx); err == nil && boundaryGJSON != "" {
		if cityAreaSqM, err := geo.BoundaryAreaSqM(boundaryGJSON); err == nil && cityAreaSqM > 0 {
			meta.CityAreaSqM = cityAreaSqM
			if totalPavedSqM > 0 {
				meta.PctPaved = totalPavedSqM / cityAreaSqM * 100
			}
		}
	}

	return meta, nil
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

	var features []map[string]any
	for _, st := range allStats {
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
// Falls back to a single cohort if no cohort stats exist.
func BuildCohortsForResource(ctx context.Context, rt resource.ResourceType, areaSqM float64, store db.Store, fc *config.ForecastConfig) []forecast.Cohort {
	currentPCI := fc.InitialPCI
	stats, _ := store.ListCohortStats(ctx, rt.Name())
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
	return cohorts
}

// ForecastExport holds per-resource forecast results.
type ForecastExport struct {
	ResourceType string                   `json:"resource_type"`
	Baseline     forecast.ScenarioResult  `json:"baseline"`
	BboxBaseline *forecast.ScenarioResult `json:"bbox_baseline,omitempty"` // full-bbox scope (shown when "All Roads" is toggled)
	Comparisons  []forecast.Comparison    `json:"comparisons"`
}

// BuildForecastsForCity builds per-resource forecast exports for a city.
// Prefers city-scoped data (excluding state/federal roads) as the primary
// baseline since that matches what a city budget covers. The full-bbox
// baseline is stored as BboxBaseline for the "All Roads" toggle.
func BuildForecastsForCity(ctx context.Context, entry CityEntry, fc *config.ForecastConfig, costTiers []forecast.CostTier) []ForecastExport {
	years := fc.Years
	doNothing := forecast.Scenario{Name: "baseline", Label: "Baseline (Do Nothing)", Strategy: forecast.StrategyDoNothing}
	var forecasts []ForecastExport

	for _, rt := range resource.All {
		result, err := entry.Store.LatestComputeResult(ctx, rt.Name())
		if err != nil || result == nil {
			continue
		}
		rtParams := forecast.NewParamsForResource(rt.Name(), fc.GrowthRate, costTiers)

		// Build full-bbox cohorts (always available)
		bboxCohorts := BuildCohortsForResource(ctx, rt, result.TotalAreaSqM, entry.Store, fc)
		bboxBaseline := forecast.Simulate(doNothing, bboxCohorts, years, rtParams.Cost, rtParams.Growth)

		// Try city-scoped cohorts — use as primary if available
		var primaryCohorts []forecast.Cohort
		var hasCityScope bool
		cityStats, _ := entry.Store.ListCohortStats(ctx, rt.Name()+":city")
		if len(cityStats) > 0 {
			var cityInputs []forecast.CohortInput
			for _, st := range cityStats {
				cityInputs = append(cityInputs, forecast.CohortInput{
					Classification: st.Classification,
					AreaSqM:        st.AreaSqM,
				})
			}
			if cityCohorts := forecast.BuildCohorts(cityInputs, fc.InitialPCI, fc.DecayRate); cityCohorts != nil {
				primaryCohorts = cityCohorts
				hasCityScope = true
			}
		}
		if primaryCohorts == nil {
			primaryCohorts = bboxCohorts
		}

		baseline := forecast.Simulate(doNothing, primaryCohorts, years, rtParams.Cost, rtParams.Growth)
		year1Need := baseline.Years[0].AnnualNeed
		comparisons := forecast.GroupedComparisons(year1Need, primaryCohorts, years,
			rtParams.Cost, rtParams.Growth)

		fe := ForecastExport{
			ResourceType: rt.Name(),
			Baseline:     baseline,
			Comparisons:  comparisons,
		}
		if hasCityScope {
			fe.BboxBaseline = &bboxBaseline
		}

		forecasts = append(forecasts, fe)
	}

	return forecasts
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
type ForecastSeedJSON struct {
	InitialPCI   float64             `json:"initial_pci"`
	DecayRate    float64             `json:"decay_rate"`
	GrowthRate   float64             `json:"growth_rate"`
	Years        int                 `json:"years"`
	TotalAreaSqM float64             `json:"total_area_sqm"`
	CityAreaSqM  float64             `json:"city_area_sqm"`
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

	var totalArea, cityArea float64
	for _, rt := range resource.All {
		result, err := store.LatestComputeResult(ctx, rt.Name())
		if err != nil || result == nil {
			continue
		}
		totalArea += result.TotalAreaSqM
		cityResult, err := store.LatestComputeResult(ctx, rt.Name()+":city")
		if err == nil && cityResult != nil {
			cityArea += cityResult.TotalAreaSqM
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
		CityAreaSqM:  cityArea,
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
		bbox, lon, lat, err := entry.BBoxAndCenter(ctx)
		if err != nil {
			return fmt.Errorf("city %s bbox: %w", entry.Slug, err)
		}
		cities = append(cities, CityInfo{
			Slug:      entry.Slug,
			Name:      entry.City.Name,
			BBox:      bbox,
			CenterLon: lon,
			CenterLat: lat,
		})
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

	// Render single top-level index.html with first city data
	firstEntry := e.entries[0]
	fc := e.cfg.ResolvedForecast(&firstEntry.City)
	seed := BuildForecastSeed(ctx, &fc, firstEntry.Store)
	meta, err := BuildMeta(ctx, firstEntry)
	if err != nil {
		return err
	}

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

	// Export each resource type as GeoJSON (prefer city-clipped variant)
	for _, rt := range resource.All {
		if err := exportResourceGeoJSON(ctx, entry.Store, rt, dataDir); err != nil {
			return err
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

// exportResourceGeoJSON writes a single resource type's GeoJSON file to dataDir.
// It prefers the city-clipped variant and silently skips if no data is available.
func exportResourceGeoJSON(ctx context.Context, store db.Store, rt resource.ResourceType, dataDir string) error {
	result, err := store.LatestComputeResult(ctx, rt.Name()+":city")
	if err != nil {
		result, err = store.LatestComputeResult(ctx, rt.Name())
	}
	if err != nil {
		return nil //nolint:nilerr // silently skip when no data is available
	}
	if result.GeometryJSON == "" {
		return nil
	}
	geojsonPath := filepath.Join(dataDir, rt.Name()+".geojson")
	fc := map[string]any{
		"type": "FeatureCollection",
		"features": []map[string]any{
			{
				"type":       "Feature",
				"geometry":   json.RawMessage(result.GeometryJSON),
				"properties": map[string]any{"type": rt.Name()},
			},
		},
	}
	if err := writeJSON(geojsonPath, fc); err != nil {
		return fmt.Errorf("write %s geojson: %w", rt.Name(), err)
	}
	return nil
}

func exportScenariosForCity(ctx context.Context, entry CityEntry, dataDir string) error {
	fc := entry.Config.ResolvedForecast(&entry.City)
	costTiers := ConvertCostTiers(&fc)

	forecasts := BuildForecastsForCity(ctx, entry, &fc, costTiers)

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
	comparisons := forecast.GroupedComparisons(year1Need, cohorts, years,
		params.Cost, params.Growth)

	scenarios := []forecast.ScenarioResult{baseline}
	for _, comp := range comparisons {
		scenarios = append(scenarios, comp.Scenarios...)
	}
	return scenarios
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

func (e *Exporter) renderHTML(meta MetaJSON, seed template.JS, rawTOML, resolvedTOML, unitSystem string, cities []CityInfo) (err error) {
	sys := units.ParseSystem(unitSystem)
	indexData, err := templatesFS.ReadFile("templates/index.html.tmpl")
	if err != nil {
		return fmt.Errorf("read template: %w", err)
	}
	tmpl, err := template.New("index").Funcs(indexFuncMap(sys)).Parse(string(indexData))
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}
	methData, err := templatesFS.ReadFile("templates/methodology.html.tmpl")
	if err != nil {
		return fmt.Errorf("read methodology template: %w", err)
	}
	if _, err := tmpl.Parse(string(methData)); err != nil {
		return fmt.Errorf("parse methodology template: %w", err)
	}
	themeData, err := templatesFS.ReadFile("templates/theme.html.tmpl")
	if err != nil {
		return fmt.Errorf("read theme template: %w", err)
	}
	if _, err := tmpl.Parse(string(themeData)); err != nil {
		return fmt.Errorf("parse theme template: %w", err)
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

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
