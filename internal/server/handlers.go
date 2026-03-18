package server

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strings"
	"time"

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

	meta := s.buildMeta()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, meta); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleDataFile serves /data/{file} endpoints dynamically.
func (s *Server) handleDataFile(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")

	switch {
	case file == "meta.json":
		s.serveMetaJSON(w)
	case file == "hexgrid.geojson":
		s.serveHexGridGeoJSON(w)
	case file == "scenarios.json":
		s.serveScenariosJSON(w)
	case file == "hex-cost-summary.json":
		s.serveHexCostSummary(w)
	case strings.HasSuffix(file, ".geojson"):
		typeName := strings.TrimSuffix(file, ".geojson")
		s.serveTypeGeoJSON(w, typeName)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) buildMeta() export.MetaJSON {
	lon, lat := s.cfg.Center()
	meta := export.MetaJSON{
		ProjectName:  s.cfg.Project.Name,
		BBox:         s.cfg.Area.BBox,
		CenterLon:    lon,
		CenterLat:    lat,
		SnapshotDate: time.Now().Format("2006-01-02"),
	}
	for _, rt := range resource.All {
		result, err := s.store.LatestComputeResult(rt.Name())
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
	return meta
}

func (s *Server) serveMetaJSON(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(s.buildMeta()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (s *Server) serveHexGridGeoJSON(w http.ResponseWriter) {
	lon0, lat0 := s.cfg.Center()
	proj := geo.NewUTMProjector(lon0, lat0)

	var allStats []db.HexStat
	for _, rt := range resource.All {
		stats, err := s.store.ListHexStats(rt.Name())
		if err != nil {
			continue
		}
		allStats = append(allStats, stats...)
	}

	fc := buildHexFC(allStats, s.cfg, proj)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(fc); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (s *Server) serveTypeGeoJSON(w http.ResponseWriter, typeName string) {
	result, err := s.store.LatestComputeResult(typeName)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	if result.GeometryJSON == "" {
		http.NotFound(w, nil)
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

func (s *Server) serveScenariosJSON(w http.ResponseWriter) {
	var costTiers []forecast.CostTier
	for _, t := range s.cfg.Forecast.CostTiers {
		costTiers = append(costTiers, forecast.CostTier{
			MinPCI:      t.MinPCI,
			MaxPCI:      t.MaxPCI,
			CostPerSqFt: t.CostPerSqFt,
			Label:       t.Label,
		})
	}
	params := forecast.NewParams(s.cfg.Forecast.DecayRate, s.cfg.Forecast.GrowthRate, costTiers)
	years := s.cfg.ForecastYears()

	var totalAreaSqFt, cityAreaSqFt float64
	var cityFeatureCount, allFeatureCount int

	for _, rt := range resource.All {
		result, err := s.store.LatestComputeResult(rt.Name())
		if err != nil || result == nil {
			continue
		}
		totalAreaSqFt += result.TotalAreaSqFt
		allFeatureCount += result.FeatureCount

		cityResult, err := s.store.LatestComputeResult(rt.Name() + ":city")
		if err == nil && cityResult != nil {
			cityAreaSqFt += cityResult.TotalAreaSqFt
			cityFeatureCount += cityResult.FeatureCount
		}
	}

	currentPCI := 85.0
	allScenarios := export.BuildScenarios(totalAreaSqFt, currentPCI, years, params)

	var cityScenarios []forecast.ScenarioResult
	if cityAreaSqFt > 0 {
		cityScenarios = export.BuildScenarios(cityAreaSqFt, currentPCI, years, params)
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

func (s *Server) serveHexCostSummary(w http.ResponseWriter) {
	var costTiers []forecast.CostTier
	for _, t := range s.cfg.Forecast.CostTiers {
		costTiers = append(costTiers, forecast.CostTier{
			MinPCI:      t.MinPCI,
			MaxPCI:      t.MaxPCI,
			CostPerSqFt: t.CostPerSqFt,
			Label:       t.Label,
		})
	}
	params := forecast.NewParams(s.cfg.Forecast.DecayRate, s.cfg.Forecast.GrowthRate, costTiers)
	years := s.cfg.ForecastYears()

	result := make(map[string]map[string]float64)
	for _, rt := range resource.All {
		cr, err := s.store.LatestComputeResult(rt.Name())
		if err != nil || cr == nil {
			continue
		}
		areaSqFt := cr.TotalAreaSqFt
		currentPCI := 85.0
		baseline := forecast.Simulate(
			forecast.Scenario{Name: "baseline", Label: "Baseline", Strategy: forecast.StrategyDoNothing},
			areaSqFt, currentPCI, years, params.PCI, params.Cost, params.Growth,
		)
		var year1Cost float64
		if len(baseline.Years) > 0 {
			year1Cost = baseline.Years[0].AnnualNeed
		}
		result[rt.Name()] = map[string]float64{
			"year1_cost":      year1Cost,
			"total_area_sqft": areaSqFt,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// buildHexFC builds a GeoJSON FeatureCollection from hex stats.
func buildHexFC(stats []db.HexStat, cfg *config.Config, proj *geo.UTMProjector) map[string]any {
	bbox := cfg.Area.BBox
	hexEdge := cfg.HexEdge()
	minX, minY, _ := proj.ToProjected(bbox[1], bbox[0])
	maxX, maxY, _ := proj.ToProjected(bbox[3], bbox[2])
	hexes := geo.HexGrid(minX, minY, maxX, maxY, hexEdge)

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
