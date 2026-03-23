package server

import (
	"encoding/json"
	"html/template"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/peterstace/simplefeatures/geom"

	"pvmt/internal/config"
	"pvmt/internal/db"
	"pvmt/internal/export"
	"pvmt/internal/forecast"
	"pvmt/internal/geo"
	"pvmt/internal/resource"
)

// handleIndex renders the export template with live data.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	if len(s.cities) == 0 {
		http.Error(w, "no cities configured", http.StatusInternalServerError)
		return
	}

	// Use first city for template rendering
	entry := s.cities[0]

	tmplData, err := export.TemplateFS().ReadFile("templates/index.html.tmpl")
	if err != nil {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}

	funcMap := template.FuncMap{
		"divf": func(a, b float64) float64 { return a / b },
	}
	tmpl, err := template.New("index").Funcs(funcMap).Parse(string(tmplData))
	if err != nil {
		http.Error(w, "template parse error", http.StatusInternalServerError)
		return
	}

	meta, err := buildMetaForCity(entry)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var rawTOML string
	if entry.Config.SourcePath != "" {
		if data, err := os.ReadFile(entry.Config.SourcePath); err == nil {
			rawTOML = string(data)
		}
	}

	fc := entry.Config.ResolvedForecast(&entry.City)

	// Build city info for template
	var cities []export.CityInfo
	for _, e := range s.cities {
		bbox, lon, lat, err := e.BBoxAndCenter()
		if err != nil {
			continue
		}
		cities = append(cities, export.CityInfo{
			Slug:      e.Slug,
			Name:      e.City.Name,
			BBox:      bbox,
			CenterLon: lon,
			CenterLat: lat,
		})
	}

	td := export.TemplateData{
		MetaJSON:     meta,
		ForecastSeed: export.BuildForecastSeedWithForecast(&fc, entry.Store),
		LayerColors:  export.ResourceColorsJS(),
		RawTOML:      rawTOML,
		ResolvedTOML: export.ResolvedTOML(entry.Config),
		Cities:       cities,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, td); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleWasmExecJS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	w.Write(export.WasmExecJS())
}

func (s *Server) handleForecastWasm(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/wasm")
	w.Write(export.ForecastWasm())
}

// handleDataFile returns a handler for /data/{file} for a specific city entry.
func (s *Server) handleDataFile(entry export.CityEntry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		file := r.PathValue("file")
		s.serveDataFile(w, r, file, entry)
	}
}

// handleCityDataFile handles /cities/{slug}/data/{file}
func (s *Server) handleCityDataFile(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	entry := s.cityBySlug(slug)
	if entry == nil {
		http.NotFound(w, r)
		return
	}
	file := r.PathValue("file")
	s.serveDataFile(w, r, file, *entry)
}

// handleCitiesList returns JSON list of all cities.
func (s *Server) handleCitiesList(w http.ResponseWriter, _ *http.Request) {
	type cityJSON struct {
		Slug      string     `json:"slug"`
		Name      string     `json:"name"`
		BBox      [4]float64 `json:"bbox"`
		CenterLon float64    `json:"center_lon"`
		CenterLat float64    `json:"center_lat"`
	}
	var cities []cityJSON
	for _, e := range s.cities {
		bbox, lon, lat, err := e.BBoxAndCenter()
		if err != nil {
			continue
		}
		cities = append(cities, cityJSON{
			Slug:      e.Slug,
			Name:      e.City.Name,
			BBox:      bbox,
			CenterLon: lon,
			CenterLat: lat,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(cities); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (s *Server) serveDataFile(w http.ResponseWriter, r *http.Request, file string, entry export.CityEntry) {
	switch {
	case file == "meta.json":
		serveMetaJSON(w, entry)
	case file == "hexgrid.geojson":
		serveHexGridGeoJSON(w, entry)
	case file == "scenarios.json":
		serveScenariosJSON(w, entry)
	case file == "forecast.json":
		serveForecastJSON(w, entry)
	case file == "forecast_seed.json":
		serveForecastSeed(w, entry)
	case file == "hex-cost-summary.json":
		serveHexCostSummary(w, entry)
	case file == "boundary.geojson":
		serveBoundaryGeoJSON(w, entry)
	case strings.HasSuffix(file, ".geojson"):
		typeName := strings.TrimSuffix(file, ".geojson")
		serveTypeGeoJSON(w, r, entry, typeName)
	default:
		http.NotFound(w, r)
	}
}

func buildMetaForCity(entry export.CityEntry) (export.MetaJSON, error) {
	bbox, lon, lat, err := entry.BBoxAndCenter()
	if err != nil {
		return export.MetaJSON{}, err
	}
	meta := export.MetaJSON{
		ProjectName:  entry.City.Name,
		BBox:         bbox,
		CenterLon:    lon,
		CenterLat:    lat,
		SnapshotDate: time.Now().Format("2006-01-02"),
	}
	for _, rt := range resource.All {
		result, err := entry.Store.LatestComputeResult(rt.Name())
		if err != nil {
			continue
		}
		meta.Stats = append(meta.Stats, export.StatJSON{
			Type:           result.ResourceType,
			Color:          export.ResourceColors[result.ResourceType],
			TotalAreaSqFt:  result.TotalAreaSqFt,
			TotalAreaAcres: result.TotalAreaAcres,
			FeatureCount:   result.FeatureCount,
		})
	}
	return meta, nil
}

func serveMetaJSON(w http.ResponseWriter, entry export.CityEntry) {
	meta, err := buildMetaForCity(entry)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(meta); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func serveHexGridGeoJSON(w http.ResponseWriter, entry export.CityEntry) {
	_, lon0, lat0, err := entry.BBoxAndCenter()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	proj := geo.NewUTMProjector(lon0, lat0)

	var allStats []db.HexStat
	for _, rt := range resource.All {
		stats, err := entry.Store.ListHexStats(rt.Name())
		if err != nil {
			continue
		}
		allStats = append(allStats, stats...)
	}

	fc := buildHexFC(allStats, entry, proj)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(fc); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func serveTypeGeoJSON(w http.ResponseWriter, r *http.Request, entry export.CityEntry, typeName string) {
	result, err := entry.Store.LatestComputeResult(typeName + ":city")
	if err != nil {
		result, err = entry.Store.LatestComputeResult(typeName)
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if result.GeometryJSON == "" {
		http.NotFound(w, r)
		return
	}
	fc := map[string]any{
		"type": "FeatureCollection",
		"features": []map[string]any{
			{
				"type":       "Feature",
				"geometry":   json.RawMessage(result.GeometryJSON),
				"properties": map[string]any{"type": typeName},
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(fc); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func serveScenariosJSON(w http.ResponseWriter, entry export.CityEntry) {
	fc := entry.Config.ResolvedForecast(&entry.City)
	costTiers := configCostTiers(&fc)
	params := forecast.NewParams(fc.GrowthRate, costTiers)
	years := fc.ResolvedYears()

	var totalAreaSqFt, cityAreaSqFt float64
	var cityFeatureCount, allFeatureCount int

	for _, rt := range resource.All {
		result, err := entry.Store.LatestComputeResult(rt.Name())
		if err != nil || result == nil {
			continue
		}
		totalAreaSqFt += result.TotalAreaSqFt
		allFeatureCount += result.FeatureCount

		cityResult, err := entry.Store.LatestComputeResult(rt.Name() + ":city")
		if err == nil && cityResult != nil {
			cityAreaSqFt += cityResult.TotalAreaSqFt
			cityFeatureCount += cityResult.FeatureCount
		}
	}

	currentPCI := 85.0
	defaultRate := forecast.DefaultDecayRates["default"]
	if fc.DecayRate > 0 {
		defaultRate = fc.DecayRate
	}
	allCohorts := []forecast.Cohort{{
		Classification: "all",
		AreaSqFt:       totalAreaSqFt,
		DecayRate:      defaultRate,
		InitialPCI:     currentPCI,
	}}
	allScenarios := export.BuildScenarios(allCohorts, years, params)

	var cityScenarios []forecast.ScenarioResult
	if cityAreaSqFt > 0 {
		cityCohorts := []forecast.Cohort{{
			Classification: "city",
			AreaSqFt:       cityAreaSqFt,
			DecayRate:      defaultRate,
			InitialPCI:     currentPCI,
		}}
		cityScenarios = export.BuildScenarios(cityCohorts, years, params)
	}

	summary := map[string]any{
		"city_count":    cityFeatureCount,
		"all_count":     allFeatureCount,
		"state_count":   0,
		"county_count":  0,
		"federal_count": 0,
	}
	if totalAreaSqFt > 0 && cityAreaSqFt > 0 {
		summary["city_pct"] = cityAreaSqFt / totalAreaSqFt
	}

	out := map[string]any{
		"all":     allScenarios,
		"summary": summary,
	}
	if cityScenarios != nil {
		out["city"] = cityScenarios
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func serveForecastJSON(w http.ResponseWriter, entry export.CityEntry) {
	fc := entry.Config.ResolvedForecast(&entry.City)
	costTiers := configCostTiers(&fc)
	years := fc.ResolvedYears()

	type forecastExport struct {
		ResourceType string                   `json:"resource_type"`
		Baseline     forecast.ScenarioResult  `json:"baseline"`
		CityBaseline *forecast.ScenarioResult `json:"city_baseline,omitempty"`
		Comparisons  []forecast.Comparison    `json:"comparisons"`
	}

	var allForecasts []forecastExport

	for _, rt := range resource.All {
		result, err := entry.Store.LatestComputeResult(rt.Name())
		if err != nil || result == nil {
			continue
		}

		cohorts := buildCohortsForResource(rt, result.TotalAreaSqFt, entry.Store, &fc)
		rtParams := forecast.NewParamsForResource(rt.Name(), fc.GrowthRate, costTiers)

		baseline := forecast.Simulate(
			forecast.Scenario{Name: "baseline", Label: "Baseline (Do Nothing)", Strategy: forecast.StrategyDoNothing},
			cohorts, years, rtParams.Cost, rtParams.Growth,
		)

		year1Need := baseline.Years[0].AnnualNeed
		comparisons := forecast.GroupedComparisons(year1Need, cohorts, years,
			rtParams.Cost, rtParams.Growth)

		fe := forecastExport{
			ResourceType: rt.Name(),
			Baseline:     baseline,
			Comparisons:  comparisons,
		}

		// Build city baseline if city cohort stats exist
		cityStats, _ := entry.Store.ListCohortStats(rt.Name() + ":city")
		if len(cityStats) > 0 {
			var cityInputs []forecast.CohortInput
			for _, st := range cityStats {
				cityInputs = append(cityInputs, forecast.CohortInput{
					Classification: st.Classification,
					AreaSqFt:       st.AreaSqFt,
				})
			}
			cityCohorts := forecast.BuildCohorts(cityInputs, 85.0, fc.DecayRate)
			if cityCohorts != nil {
				cityBaseline := forecast.Simulate(
					forecast.Scenario{Name: "baseline", Label: "Baseline (Do Nothing)", Strategy: forecast.StrategyDoNothing},
					cityCohorts, years, rtParams.Cost, rtParams.Growth,
				)
				fe.CityBaseline = &cityBaseline
			}
		}

		allForecasts = append(allForecasts, fe)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(allForecasts); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func serveHexCostSummary(w http.ResponseWriter, entry export.CityEntry) {
	fc := entry.Config.ResolvedForecast(&entry.City)
	costTiers := configCostTiers(&fc)
	years := fc.ResolvedYears()

	result := make(map[string]map[string]float64)
	for _, rt := range resource.All {
		cr, err := entry.Store.LatestComputeResult(rt.Name())
		if err != nil || cr == nil {
			continue
		}

		cohorts := buildCohortsForResource(rt, cr.TotalAreaSqFt, entry.Store, &fc)
		rtParams := forecast.NewParamsForResource(rt.Name(), fc.GrowthRate, costTiers)
		baseline := forecast.Simulate(
			forecast.Scenario{Name: "baseline", Label: "Baseline", Strategy: forecast.StrategyDoNothing},
			cohorts, years, rtParams.Cost, rtParams.Growth,
		)
		var year1Cost float64
		if len(baseline.Years) > 0 {
			year1Cost = baseline.Years[0].AnnualNeed
		}
		result[rt.Name()] = map[string]float64{
			"year1_cost":      year1Cost,
			"total_area_sqft": cr.TotalAreaSqFt,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// configCostTiers converts config forecast cost tiers to forecast.CostTier slice.
func configCostTiers(fc *config.ForecastConfig) []forecast.CostTier {
	var tiers []forecast.CostTier
	for _, t := range fc.CostTiers {
		tiers = append(tiers, forecast.CostTier{
			MinPCI:      t.MinPCI,
			MaxPCI:      t.MaxPCI,
			CostPerSqFt: t.CostPerSqFt,
			Label:       t.Label,
		})
	}
	return tiers
}

// buildCohortsForResource builds forecast cohorts for a resource type from the store.
// Falls back to a single cohort if no cohort stats exist.
func buildCohortsForResource(rt resource.ResourceType, areaSqFt float64, store db.Store, fc *config.ForecastConfig) []forecast.Cohort {
	currentPCI := 85.0
	stats, _ := store.ListCohortStats(rt.Name())
	var inputs []forecast.CohortInput
	for _, st := range stats {
		inputs = append(inputs, forecast.CohortInput{
			Classification: st.Classification,
			AreaSqFt:       st.AreaSqFt,
		})
	}
	cohorts := forecast.BuildCohorts(inputs, currentPCI, fc.DecayRate)
	if cohorts == nil {
		defaultRate := forecast.DecayRateForClass(rt.Name())
		if fc.DecayRate > 0 {
			defaultRate = fc.DecayRate
		}
		cohorts = []forecast.Cohort{{
			Classification: rt.Name(),
			AreaSqFt:       areaSqFt,
			DecayRate:      defaultRate,
			InitialPCI:     currentPCI,
		}}
	}
	return cohorts
}


func serveBoundaryGeoJSON(w http.ResponseWriter, entry export.CityEntry) {
	gj, err := entry.Store.GetBoundary()
	if err != nil || gj == "" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"FeatureCollection","features":[]}`))
		return
	}
	fc := map[string]any{
		"type": "FeatureCollection",
		"features": []map[string]any{
			{
				"type":       "Feature",
				"geometry":   json.RawMessage(gj),
				"properties": map[string]any{"type": "boundary"},
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(fc); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func serveForecastSeed(w http.ResponseWriter, entry export.CityEntry) {
	fc := entry.Config.ResolvedForecast(&entry.City)
	seed := export.BuildForecastSeedWithForecast(&fc, entry.Store)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(seed))
}

// buildHexFC builds a GeoJSON FeatureCollection from hex stats.
func buildHexFC(stats []db.HexStat, entry export.CityEntry, proj *geo.UTMProjector) map[string]any {
	bbox, _, _, _ := entry.BBoxAndCenter()
	hexEdge := entry.Config.ResolvedHexEdge(&entry.City)
	minX, minY, _ := proj.ToProjected(bbox[1], bbox[0])
	maxX, maxY, _ := proj.ToProjected(bbox[3], bbox[2])
	hexes := geo.HexGrid(minX, minY, maxX, maxY, hexEdge)

	// Clip hex grid to city boundary
	if boundaryGJSON, err := entry.Store.GetBoundary(); err == nil && boundaryGJSON != "" {
		boundaryGeom, _, gErr := geo.GeoJSONToProjectedGeometry(boundaryGJSON, proj)
		if gErr == nil && !boundaryGeom.IsEmpty() {
			filtered := make([]geo.Hex, 0, len(hexes))
			for _, h := range hexes {
				inter, iErr := geom.Intersection(h.Geom, boundaryGeom)
				if iErr == nil && !inter.IsEmpty() {
					h.Geom = inter
					filtered = append(filtered, h)
				}
			}
			hexes = filtered
		}
	}

	hexMap := make(map[string]*geo.Hex, len(hexes))
	for i := range hexes {
		hexMap[hexes[i].ID] = &hexes[i]
	}

	type hexFeature struct {
		Type       string          `json:"type"`
		Geometry   json.RawMessage `json:"geometry"`
		Properties map[string]any  `json:"properties"`
	}

	var features []hexFeature
	for _, st := range stats {
		h, ok := hexMap[st.HexID]
		if !ok {
			continue
		}
		gjson, err := geo.GeometryToGeoJSON(h.Geom, proj)
		if err != nil {
			continue
		}
		features = append(features, hexFeature{
			Type:     "Feature",
			Geometry: json.RawMessage(gjson),
			Properties: map[string]any{
				"hex_id":        st.HexID,
				"resource_type": st.ResourceType,
				"area_sqft":     st.AreaSqFt,
				"pct_covered":   st.PctCovered,
			},
		})
	}

	return map[string]any{
		"type":     "FeatureCollection",
		"features": features,
	}
}
