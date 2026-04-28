package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"

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

	ctx := r.Context()

	// Use first city for template rendering
	entry := s.cities[0]

	tmpl, err := export.ParseIndexTemplate(entry.Config.UnitSystem())
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

	// Build city info for template. Only populate in multi-city mode so the
	// frontend's DATA_PREFIX stays empty and matches the /data/{file} routes
	// registered by the single-city server branch (mirrors the static exporter).
	var cities []export.CityInfo
	if len(s.cities) > 1 {
		for _, e := range s.cities {
			info, err := e.Info(ctx)
			if err != nil {
				continue
			}
			cities = append(cities, info)
		}
	}

	td := export.TemplateData{
		MetaJSON:        meta,
		ForecastSeed:    export.BuildForecastSeed(ctx, &fc, entry.Store),
		LayerColors:     export.ResourceColorsJS(),
		RawTOML:         rawTOML,
		ResolvedTOML:    export.ResolvedTOML(entry.Config),
		UnitSystem:      entry.Config.UnitSystem().String(),
		Cities:          cities,
		MethodologyHTML: export.MethodologyHTML(),
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
		info, err := e.Info(ctx)
		if err != nil {
			continue
		}
		cities = append(cities, info)
	}
	writeJSON(w, cities)
}

func (s *Server) serveDataFile(w http.ResponseWriter, r *http.Request, file string, entry export.CityEntry) {
	switch file {
	case "meta.json":
		s.serveMetaJSON(w, r, entry)
	case "hexgrid.geojson":
		s.serveHexGridGeoJSON(w, r, entry)
	case "scenarios.json":
		s.serveScenariosJSON(w, r, entry)
	case "forecast.json":
		s.serveForecastJSON(w, r, entry)
	case "forecast_seed.json":
		s.serveForecastSeed(w, r, entry)
	case "hex-cost-summary.json":
		s.serveHexCostSummary(w, r, entry)
	case "boundary.geojson":
		s.serveBoundaryGeoJSON(w, r, entry)
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

// serveJSONCached short-circuits on a cache hit; otherwise it invokes build
// and caches the resulting value. build returns nil to signal "no error but
// no value" — callers that need a different empty-shape can write their own
// handler.
func (s *Server) serveJSONCached(w http.ResponseWriter, key string, build func() (any, error)) {
	if s.serveCached(w, key) {
		return
	}
	v, err := build()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSONCached(w, key, v)
}

func (s *Server) serveMetaJSON(w http.ResponseWriter, r *http.Request, entry export.CityEntry) {
	s.serveJSONCached(w, "meta:"+entry.Slug, func() (any, error) {
		return export.BuildMeta(r.Context(), entry)
	})
}

func (s *Server) serveHexGridGeoJSON(w http.ResponseWriter, r *http.Request, entry export.CityEntry) {
	s.serveJSONCached(w, "hexgrid:"+entry.Slug, func() (any, error) {
		ctx := r.Context()
		_, lon0, lat0, err := entry.BBoxAndCenter(ctx)
		if err != nil {
			return nil, err
		}
		fc := export.BuildHexGeoJSON(ctx, entry, geo.NewUTMProjector(lon0, lat0))
		if fc == nil {
			fc = map[string]any{"type": "FeatureCollection", "features": []any{}}
		}
		return fc, nil
	})
}

func (s *Server) serveScenariosJSON(w http.ResponseWriter, r *http.Request, entry export.CityEntry) {
	s.serveJSONCached(w, "scenarios:"+entry.Slug, func() (any, error) {
		fc := entry.Config.ResolvedForecast(&entry.City)
		return export.BuildScenariosData(r.Context(), entry, &fc), nil
	})
}

// buildForecasts returns the per-city forecast list, memoized so that
// serveForecastJSON and serveHexCostSummary share a single computation.
func (s *Server) buildForecasts(ctx context.Context, entry export.CityEntry) []export.ForecastExport {
	if cached, ok := s.forecasts.Load(entry.Slug); ok {
		if forecasts, ok := cached.([]export.ForecastExport); ok {
			return forecasts
		}
	}
	fc := entry.Config.ResolvedForecast(&entry.City)
	forecasts := export.BuildForecastsForCity(ctx, entry, &fc, export.ConvertCostTiers(&fc))
	s.forecasts.Store(entry.Slug, forecasts)
	return forecasts
}

func (s *Server) serveForecastJSON(w http.ResponseWriter, r *http.Request, entry export.CityEntry) {
	s.serveJSONCached(w, "forecast:"+entry.Slug, func() (any, error) {
		return s.buildForecasts(r.Context(), entry), nil
	})
}

func (s *Server) serveHexCostSummary(w http.ResponseWriter, r *http.Request, entry export.CityEntry) {
	s.serveJSONCached(w, "hexcost:"+entry.Slug, func() (any, error) {
		ctx := r.Context()
		return export.BuildHexCostSummary(ctx, entry, s.buildForecasts(ctx, entry)), nil
	})
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
	s.serveJSONCached(w, "seed:"+entry.Slug, func() (any, error) {
		fc := entry.Config.ResolvedForecast(&entry.City)
		return json.RawMessage(export.BuildForecastSeed(r.Context(), &fc, entry.Store)), nil
	})
}
