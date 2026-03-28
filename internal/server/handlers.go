package server

import (
	"encoding/json"
	"html/template"
	"net/http"
	"os"
	"strings"

	"pvmt/internal/export"
	"pvmt/internal/geo"
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

	meta, err := export.BuildMeta(entry)
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
		ForecastSeed: export.BuildForecastSeed(&fc, entry.Store),
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
	writeJSON(w, cities)
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

// writeJSON encodes v as JSON and writes it to w with appropriate headers.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func serveMetaJSON(w http.ResponseWriter, entry export.CityEntry) {
	meta, err := export.BuildMeta(entry)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, meta)
}

func serveHexGridGeoJSON(w http.ResponseWriter, entry export.CityEntry) {
	_, lon0, lat0, err := entry.BBoxAndCenter()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	proj := geo.NewUTMProjector(lon0, lat0)

	fc := export.BuildHexGeoJSON(entry, proj)
	if fc == nil {
		fc = map[string]any{"type": "FeatureCollection", "features": []any{}}
	}
	writeJSON(w, fc)
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
	writeJSON(w, map[string]any{
		"type": "FeatureCollection",
		"features": []map[string]any{
			{
				"type":       "Feature",
				"geometry":   json.RawMessage(result.GeometryJSON),
				"properties": map[string]any{"type": typeName},
			},
		},
	})
}

func serveScenariosJSON(w http.ResponseWriter, entry export.CityEntry) {
	fc := entry.Config.ResolvedForecast(&entry.City)
	writeJSON(w, export.BuildScenariosData(entry, &fc))
}

func serveForecastJSON(w http.ResponseWriter, entry export.CityEntry) {
	fc := entry.Config.ResolvedForecast(&entry.City)
	costTiers := export.ConvertCostTiers(&fc)
	writeJSON(w, export.BuildForecastsForCity(entry, &fc, costTiers))
}

func serveHexCostSummary(w http.ResponseWriter, entry export.CityEntry) {
	fc := entry.Config.ResolvedForecast(&entry.City)
	costTiers := export.ConvertCostTiers(&fc)
	forecasts := export.BuildForecastsForCity(entry, &fc, costTiers)
	writeJSON(w, export.BuildHexCostSummary(entry, forecasts))
}

func serveBoundaryGeoJSON(w http.ResponseWriter, entry export.CityEntry) {
	gj, err := entry.Store.GetBoundary()
	if err != nil || gj == "" {
		writeJSON(w, map[string]any{"type": "FeatureCollection", "features": []any{}})
		return
	}
	writeJSON(w, map[string]any{
		"type": "FeatureCollection",
		"features": []map[string]any{
			{
				"type":       "Feature",
				"geometry":   json.RawMessage(gj),
				"properties": map[string]any{"type": "boundary"},
			},
		},
	})
}

func serveForecastSeed(w http.ResponseWriter, entry export.CityEntry) {
	fc := entry.Config.ResolvedForecast(&entry.City)
	seed := export.BuildForecastSeed(&fc, entry.Store)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(seed))
}
