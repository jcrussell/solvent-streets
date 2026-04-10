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
		serveMetaJSON(w, r, entry)
	case file == "hexgrid.geojson":
		serveHexGridGeoJSON(w, r, entry)
	case file == "scenarios.json":
		serveScenariosJSON(w, r, entry)
	case file == "forecast.json":
		serveForecastJSON(w, r, entry)
	case file == "forecast_seed.json":
		serveForecastSeed(w, r, entry)
	case file == "hex-cost-summary.json":
		serveHexCostSummary(w, r, entry)
	case file == "boundary.geojson":
		serveBoundaryGeoJSON(w, r, entry)
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

func serveMetaJSON(w http.ResponseWriter, r *http.Request, entry export.CityEntry) {
	meta, err := export.BuildMeta(r.Context(), entry)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, meta)
}

func serveHexGridGeoJSON(w http.ResponseWriter, r *http.Request, entry export.CityEntry) {
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
	writeJSON(w, fc)
}

func serveTypeGeoJSON(w http.ResponseWriter, r *http.Request, entry export.CityEntry, typeName string) {
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

func serveScenariosJSON(w http.ResponseWriter, r *http.Request, entry export.CityEntry) {
	fc := entry.Config.ResolvedForecast(&entry.City)
	writeJSON(w, export.BuildScenariosData(r.Context(), entry, &fc))
}

func serveForecastJSON(w http.ResponseWriter, r *http.Request, entry export.CityEntry) {
	ctx := r.Context()
	fc := entry.Config.ResolvedForecast(&entry.City)
	costTiers := export.ConvertCostTiers(&fc)
	writeJSON(w, export.BuildForecastsForCity(ctx, entry, &fc, costTiers))
}

func serveHexCostSummary(w http.ResponseWriter, r *http.Request, entry export.CityEntry) {
	ctx := r.Context()
	fc := entry.Config.ResolvedForecast(&entry.City)
	costTiers := export.ConvertCostTiers(&fc)
	forecasts := export.BuildForecastsForCity(ctx, entry, &fc, costTiers)
	writeJSON(w, export.BuildHexCostSummary(ctx, entry, forecasts))
}

func serveBoundaryGeoJSON(w http.ResponseWriter, r *http.Request, entry export.CityEntry) {
	gj, err := entry.Store.GetBoundary(r.Context())
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

func serveForecastSeed(w http.ResponseWriter, r *http.Request, entry export.CityEntry) {
	fc := entry.Config.ResolvedForecast(&entry.City)
	seed := export.BuildForecastSeed(r.Context(), &fc, entry.Store)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(seed))
}
