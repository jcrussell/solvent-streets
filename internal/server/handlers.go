package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sync"

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

// jsonThunk wraps a sync.OnceValues thunk so s.cache stores pointer-comparable
// values — sync.Map.CompareAndDelete uses == internally, which panics on
// uncomparable types like raw function values.
type jsonThunk struct {
	once func() ([]byte, error)
}

// forecastThunk is the equivalent wrapper for s.forecasts. See jsonThunk.
type forecastThunk struct {
	once func() ([]export.ForecastExport, error)
}

// serveJSONCached runs build at most once per key — concurrent first callers
// single-flight via sync.OnceValues against s.cache. Build closures must use
// context.Background(), not the request context: the first arriver's build
// outlives their request, and any later arriver waiting on the OnceValues
// thunk gets that same result. Tying the build to a request ctx would let
// the first arriver's disconnect cancel the build for everyone — and for
// builds that swallow ctx errors (e.g. BuildForecastsForCity skips per-
// resource errors silently) it would even let a dropped client poison the
// cache with a partial slice. Successful results stay cached for the
// server's lifetime ("never invalidated" — restart for fresh data); errors
// and panics are evicted so the next request retries.
func (s *Server) serveJSONCached(w http.ResponseWriter, key string, build func() (any, error)) {
	var entry *jsonThunk
	if v, ok := s.cache.Load(key); ok {
		entry = v.(*jsonThunk) //nolint:forcetypeassert // type invariant: only this site Stores into s.cache
	} else {
		fresh := &jsonThunk{once: sync.OnceValues(func() ([]byte, error) {
			v, err := build()
			if err != nil {
				return nil, err
			}
			return json.Marshal(v)
		})}
		actual, _ := s.cache.LoadOrStore(key, fresh)
		entry = actual.(*jsonThunk) //nolint:forcetypeassert // type invariant: only this site Stores into s.cache
	}

	// sync.OnceValues re-panics on every call after the first panic, so a
	// panicking build would otherwise poison this key forever. Evict on
	// panic and re-raise so recoveryMiddleware logs and writes the 500.
	defer func() {
		if r := recover(); r != nil {
			s.cache.CompareAndDelete(key, entry)
			panic(r)
		}
	}()

	data, err := entry.once()
	if err != nil {
		s.cache.CompareAndDelete(key, entry)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Write(data)
}

func (s *Server) serveMetaJSON(w http.ResponseWriter, _ *http.Request, entry export.CityEntry) {
	s.serveJSONCached(w, "meta:"+entry.Slug, func() (any, error) {
		return export.BuildMeta(context.Background(), entry)
	})
}

func (s *Server) serveHexGridGeoJSON(w http.ResponseWriter, _ *http.Request, entry export.CityEntry) {
	s.serveJSONCached(w, "hexgrid:"+entry.Slug, func() (any, error) {
		_, lon0, lat0, err := entry.BBoxAndCenter(context.Background())
		if err != nil {
			return nil, err
		}
		fc := export.BuildHexGeoJSON(context.Background(), entry, geo.NewUTMProjector(lon0, lat0))
		if fc == nil {
			fc = map[string]any{"type": "FeatureCollection", "features": []any{}}
		}
		return fc, nil
	})
}

func (s *Server) serveScenariosJSON(w http.ResponseWriter, _ *http.Request, entry export.CityEntry) {
	s.serveJSONCached(w, "scenarios:"+entry.Slug, func() (any, error) {
		fc := entry.Config.ResolvedForecast(&entry.City)
		return export.BuildScenariosData(context.Background(), entry, &fc), nil
	})
}

// buildForecasts returns the per-city forecast list, single-flighted via
// sync.OnceValues and shared by serveForecastJSON and serveHexCostSummary.
// See serveJSONCached for why builds run against context.Background().
//
// An error here evicts the thunk so the next request retries — sync.OnceValues
// makes errors sticky for the thunk's lifetime, so without eviction a transient
// DB hiccup would surface as a permanent 500 until the server restarted.
// A panic evicts both this thunk and (after re-panic propagates up through
// serveJSONCached's OnceValues) the outer s.cache thunk — that stacked
// eviction is intentional so the next request rebuilds both layers instead
// of one rebuilding atop a cached panic in the other.
func (s *Server) buildForecasts(entry export.CityEntry) ([]export.ForecastExport, error) {
	var ft *forecastThunk
	if v, ok := s.forecasts.Load(entry.Slug); ok {
		ft = v.(*forecastThunk) //nolint:forcetypeassert // type invariant: only this site Stores into s.forecasts
	} else {
		fresh := &forecastThunk{once: sync.OnceValues(func() ([]export.ForecastExport, error) {
			fc := entry.Config.ResolvedForecast(&entry.City)
			return export.BuildForecastsForCity(context.Background(), entry, &fc, export.ConvertCostTiers(&fc))
		})}
		actual, _ := s.forecasts.LoadOrStore(entry.Slug, fresh)
		ft = actual.(*forecastThunk) //nolint:forcetypeassert // type invariant: only this site Stores into s.forecasts
	}

	// Match serveJSONCached's panic-evict semantics so a panicking forecast
	// build doesn't permanently poison the city's forecast cache.
	defer func() {
		if r := recover(); r != nil {
			s.forecasts.CompareAndDelete(entry.Slug, ft)
			panic(r)
		}
	}()
	forecasts, err := ft.once()
	if err != nil {
		s.forecasts.CompareAndDelete(entry.Slug, ft)
		return nil, err
	}
	return forecasts, nil
}

func (s *Server) serveForecastJSON(w http.ResponseWriter, _ *http.Request, entry export.CityEntry) {
	s.serveJSONCached(w, "forecast:"+entry.Slug, func() (any, error) {
		return s.buildForecasts(entry)
	})
}

func (s *Server) serveHexCostSummary(w http.ResponseWriter, _ *http.Request, entry export.CityEntry) {
	s.serveJSONCached(w, "hexcost:"+entry.Slug, func() (any, error) {
		forecasts, err := s.buildForecasts(entry)
		if err != nil {
			return nil, err
		}
		return export.BuildHexCostSummary(context.Background(), entry, forecasts), nil
	})
}

func (s *Server) serveBoundaryGeoJSON(w http.ResponseWriter, _ *http.Request, entry export.CityEntry) {
	s.serveJSONCached(w, "boundary:"+entry.Slug, func() (any, error) {
		// GetBoundary distinguishes "no row" (returns "", nil — genuinely
		// unconfigured, cache the empty FC) from real DB errors (returns
		// "", err — surface so serveJSONCached evicts and the next request
		// retries instead of locking in an empty boundary for the server's
		// lifetime).
		gj, err := entry.Store.GetBoundary(context.Background())
		if err != nil {
			return nil, err
		}
		if gj == "" {
			return map[string]any{"type": "FeatureCollection", "features": []any{}}, nil
		}
		return map[string]any{
			"type": "FeatureCollection",
			"features": []map[string]any{
				{
					"type":       "Feature",
					"geometry":   json.RawMessage(gj),
					"properties": map[string]any{"type": "boundary"},
				},
			},
		}, nil
	})
}

func (s *Server) serveForecastSeed(w http.ResponseWriter, _ *http.Request, entry export.CityEntry) {
	s.serveJSONCached(w, "seed:"+entry.Slug, func() (any, error) {
		fc := entry.Config.ResolvedForecast(&entry.City)
		return json.RawMessage(export.BuildForecastSeed(context.Background(), &fc, entry.Store)), nil
	})
}
