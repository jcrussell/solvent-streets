package server

import (
	"encoding/json"
	"html/template"
	"net/http"
	"os"
	"strings"

	"pvmt/internal/export"
	"pvmt/internal/geo"
	"pvmt/internal/units"
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

	ctx := r.Context()

	// Use first city for template rendering
	entry := s.cities[0]

	tmplData, err := export.TemplateFS().ReadFile("templates/index.html.tmpl")
	if err != nil {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}

	sys := entry.Config.UnitSystem()
	funcMap := template.FuncMap{
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
	tmpl, err := template.New("index").Funcs(funcMap).Parse(string(tmplData))
	if err != nil {
		http.Error(w, "template parse error", http.StatusInternalServerError)
		return
	}

	meta, err := export.BuildMeta(ctx, entry)
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
		bbox, lon, lat, err := e.BBoxAndCenter(ctx)
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
		ForecastSeed: export.BuildForecastSeed(ctx, &fc, entry.Store),
		LayerColors:  export.ResourceColorsJS(),
		RawTOML:      rawTOML,
		ResolvedTOML: export.ResolvedTOML(entry.Config),
		UnitSystem:   entry.Config.UnitSystem().String(),
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
func (s *Server) handleCitiesList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var cities []export.CityInfo
	for _, e := range s.cities {
		bbox, lon, lat, err := e.BBoxAndCenter(ctx)
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
		s.serveMetaJSON(w, r, entry)
	case file == "hexgrid.geojson":
		s.serveHexGridGeoJSON(w, r, entry)
	case file == "scenarios.json":
		s.serveScenariosJSON(w, r, entry)
	case file == "forecast.json":
		s.serveForecastJSON(w, r, entry)
	case file == "forecast_seed.json":
		s.serveForecastSeed(w, r, entry)
	case file == "hex-cost-summary.json":
		s.serveHexCostSummary(w, r, entry)
	case file == "boundary.geojson":
		s.serveBoundaryGeoJSON(w, r, entry)
	case strings.HasSuffix(file, ".geojson"):
		typeName := strings.TrimSuffix(file, ".geojson")
		s.serveTypeGeoJSON(w, r, entry, typeName)
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

// serveCached writes a previously cached response if available. Returns true
// if the cache was hit and the response was written.
func (s *Server) serveCached(w http.ResponseWriter, key string) bool {
	cached, ok := s.cache.Load(key)
	if !ok {
		return false
	}
	data, ok := cached.([]byte)
	if !ok {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Write(data)
	return true
}

// writeJSONCached is like writeJSON but caches the serialized response in
// the server's in-memory cache. Data endpoints don't change while the server
// is running, so this avoids redundant DB queries and JSON serialization.
func (s *Server) writeJSONCached(w http.ResponseWriter, key string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.cache.Store(key, data)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Write(data)
}

func (s *Server) serveMetaJSON(w http.ResponseWriter, r *http.Request, entry export.CityEntry) {
	key := "meta:" + entry.Slug
	if s.serveCached(w, key) {
		return
	}
	meta, err := export.BuildMeta(r.Context(), entry)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSONCached(w, key, meta)
}

func (s *Server) serveHexGridGeoJSON(w http.ResponseWriter, r *http.Request, entry export.CityEntry) {
	key := "hexgrid:" + entry.Slug
	if s.serveCached(w, key) {
		return
	}
	ctx := r.Context()
	_, lon0, lat0, err := entry.BBoxAndCenter(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	proj := geo.NewUTMProjector(lon0, lat0)

	fc := export.BuildHexGeoJSON(ctx, entry, proj)
	if fc == nil {
		fc = map[string]any{"type": "FeatureCollection", "features": []any{}}
	}
	s.writeJSONCached(w, key, fc)
}

func (s *Server) serveTypeGeoJSON(w http.ResponseWriter, r *http.Request, entry export.CityEntry, typeName string) {
	key := typeName + ":" + entry.Slug
	if s.serveCached(w, key) {
		return
	}
	ctx := r.Context()
	result, err := entry.Store.LatestComputeResult(ctx, typeName+":city")
	if err != nil {
		result, err = entry.Store.LatestComputeResult(ctx, typeName)
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if result.GeometryJSON == "" {
		http.NotFound(w, r)
		return
	}
	s.writeJSONCached(w, key, map[string]any{
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

func (s *Server) serveScenariosJSON(w http.ResponseWriter, r *http.Request, entry export.CityEntry) {
	key := "scenarios:" + entry.Slug
	if s.serveCached(w, key) {
		return
	}
	fc := entry.Config.ResolvedForecast(&entry.City)
	s.writeJSONCached(w, key, export.BuildScenariosData(r.Context(), entry, &fc))
}

func (s *Server) serveForecastJSON(w http.ResponseWriter, r *http.Request, entry export.CityEntry) {
	key := "forecast:" + entry.Slug
	if s.serveCached(w, key) {
		return
	}
	ctx := r.Context()
	fc := entry.Config.ResolvedForecast(&entry.City)
	costTiers := export.ConvertCostTiers(&fc)
	s.writeJSONCached(w, key, export.BuildForecastsForCity(ctx, entry, &fc, costTiers))
}

func (s *Server) serveHexCostSummary(w http.ResponseWriter, r *http.Request, entry export.CityEntry) {
	key := "hexcost:" + entry.Slug
	if s.serveCached(w, key) {
		return
	}
	ctx := r.Context()
	fc := entry.Config.ResolvedForecast(&entry.City)
	costTiers := export.ConvertCostTiers(&fc)
	forecasts := export.BuildForecastsForCity(ctx, entry, &fc, costTiers)
	s.writeJSONCached(w, key, export.BuildHexCostSummary(ctx, entry, forecasts))
}

func (s *Server) serveBoundaryGeoJSON(w http.ResponseWriter, r *http.Request, entry export.CityEntry) {
	key := "boundary:" + entry.Slug
	if s.serveCached(w, key) {
		return
	}
	gj, err := entry.Store.GetBoundary(r.Context())
	if err != nil || gj == "" {
		writeJSON(w, map[string]any{"type": "FeatureCollection", "features": []any{}})
		return
	}
	s.writeJSONCached(w, key, map[string]any{
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

func (s *Server) serveForecastSeed(w http.ResponseWriter, r *http.Request, entry export.CityEntry) {
	key := "seed:" + entry.Slug
	if s.serveCached(w, key) {
		return
	}
	fc := entry.Config.ResolvedForecast(&entry.City)
	seed := export.BuildForecastSeed(r.Context(), &fc, entry.Store)
	s.writeJSONCached(w, key, json.RawMessage(seed))
}
