package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/internal/geo"
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

	// Pick the first city whose template data builds successfully rather than
	// anchoring on s.cities[0]: a broken first city (e.g. no boundary ingested
	// yet) would otherwise 500 the whole site and hide every healthy city. The
	// entry must be chosen before ParseIndexTemplate since the template depends
	// on entry.Config.UnitSystem(). 500 only when no city renders.
	entry, td, err := s.firstRenderableCity(r.Context())
	if err != nil {
		s.httpErr(w, err, http.StatusInternalServerError)
		return
	}

	tmpl, err := export.ParseIndexTemplate(entry.Config.UnitSystem())
	if err != nil {
		s.httpErr(w, err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, td); err != nil {
		s.httpErr(w, err, http.StatusInternalServerError)
	}
}

// firstRenderableCity returns the first city entry whose buildIndexData
// succeeds, along with its assembled TemplateData. It mirrors the
// continue-past-broken-cities tolerance the rest of the multi-city surface
// already has (buildIndexData's cities loop, handleCitiesList, the static
// exporter). Returns the last build error only when no city renders, so a
// single broken city no longer 500s the entire site.
func (s *Server) firstRenderableCity(ctx context.Context) (export.CityEntry, export.TemplateData, error) {
	var lastErr error
	for _, entry := range s.cities {
		td, err := s.buildIndexData(ctx, entry)
		if err != nil {
			lastErr = err
			fmt.Fprintf(s.ios.ErrOut, "server: skipping city %s for index: %v\n", entry.Slug, err)
			continue
		}
		return entry, td, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no cities configured")
	}
	return export.CityEntry{}, export.TemplateData{}, lastErr
}

// buildIndexData assembles the TemplateData for handleIndex. Multi-city
// cities list is populated only when len(s.cities) > 1 so the static
// single-city DATA_PREFIX wiring keeps matching the /data/{file} routes.
func (s *Server) buildIndexData(ctx context.Context, entry export.CityEntry) (export.TemplateData, error) {
	meta, err := export.BuildMeta(ctx, entry)
	if err != nil {
		return export.TemplateData{}, err
	}

	var rawTOML string
	if entry.Config.SourcePath != "" {
		if data, err := os.ReadFile(entry.Config.SourcePath); err == nil {
			rawTOML = string(data)
		}
	}

	fc := entry.Config.ResolvedForecast(&entry.City)

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

	seed, err := export.BuildForecastSeed(ctx, &fc, entry.Store)
	if err != nil {
		return export.TemplateData{}, err
	}
	methodology, err := export.MethodologyHTML()
	if err != nil {
		return export.TemplateData{}, err
	}
	date, ver := export.FooterInfo()
	return export.TemplateData{
		MetaJSON:        meta,
		ForecastSeed:    seed,
		LayerColors:     export.ResourceColorsJS(),
		RawTOML:         rawTOML,
		ResolvedTOML:    export.ResolvedTOML(entry.Config),
		UnitSystem:      entry.Config.UnitSystem().String(),
		Cities:          cities,
		MethodologyHTML: methodology,
		IsLiveServer:    true,
		GeneratedDate:   date,
		BuildVersion:    ver,
	}, nil
}

// httpErr logs the full err server-side and writes a generic status-matched
// message to the client. The split exists so DB paths, file paths, and
// wrapped error chains stay out of the response body while operators still
// see the real cause in the server log.
//
//nolint:unparam // every current caller is 500, but the helper is the seam for any 4xx/5xx surface.
func (s *Server) httpErr(w http.ResponseWriter, err error, code int) {
	fmt.Fprintf(s.ios.ErrOut, "server: %d %s: %v\n", code, http.StatusText(code), err)
	http.Error(w, http.StatusText(code), code)
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
			fmt.Fprintf(s.ios.ErrOut, "server: skipping city %s in /api/cities: %v\n", e.Slug, err)
			continue
		}
		cities = append(cities, info)
	}
	// Emit [] rather than null when every city skips, matching
	// serveSnapshotsJSON's nil-guard and the static cities.json path so a
	// consumer iterating the list never hits a null (server/static parity).
	if cities == nil {
		cities = []export.CityInfo{}
	}
	s.writeJSON(w, cities)
}

// handleSnapshotsList returns a handler for the single-city /api/snapshots route.
func (s *Server) handleSnapshotsList(entry export.CityEntry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.serveSnapshotsJSON(w, r, entry)
	}
}

// handleCitySnapshotsList handles /api/cities/{slug}/snapshots
func (s *Server) handleCitySnapshotsList(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	entry := s.cityBySlug(slug)
	if entry == nil {
		http.NotFound(w, r)
		return
	}
	s.serveSnapshotsJSON(w, r, *entry)
}

func (s *Server) serveDataFile(w http.ResponseWriter, r *http.Request, file string, entry export.CityEntry) {
	snapshotID, ok := s.parseSnapshotParam(r.Context(), w, r, entry.Store)
	if !ok {
		return
	}
	if snapshotID > 0 {
		// Guard against serving a config-mismatched pinned snapshot: the hex
		// grid (and every other data file's hex_id namespace) is regenerated
		// from the CURRENT config, so a snapshot computed under a different
		// config (e.g. a different hex_edge_m) would have stored hex IDs that
		// no longer match — buildHexFeature silently drops the rows and the
		// client gets an empty/mislocated layer with HTTP 200, cached for the
		// server's lifetime. Fail loud instead. Only an explicitly pinned id
		// is checked; the default (latest) path is auto-scoped to the current
		// config hash by BuildCityEntries' WithConfigHash pin, so it can't
		// mismatch. RequireMatchingSnapshot only asks "does ANY snapshot match
		// the current hash" and so can't answer "does THIS pinned id match",
		// hence the dedicated check here.
		if !s.snapshotMatchesConfig(r.Context(), w, entry, snapshotID) {
			return
		}
		entry = entry.WithSnapshot(snapshotID)
	}
	switch file {
	case "meta.json":
		s.serveMetaJSON(w, r, entry, snapshotID)
	case "hexgrid.geojson":
		s.serveHexGridGeoJSON(w, r, entry, snapshotID)
	case "scenarios.json":
		s.serveScenariosJSON(w, r, entry, snapshotID)
	case "forecast.json":
		s.serveForecastJSON(w, r, entry, snapshotID)
	case "forecast_seed.json":
		s.serveForecastSeed(w, r, entry, snapshotID)
	case "hex-cost-summary.json":
		s.serveHexCostSummary(w, r, entry, snapshotID)
	case "boundary.geojson":
		s.serveBoundaryGeoJSON(w, r, entry, snapshotID)
	default:
		http.NotFound(w, r)
	}
}

// parseSnapshotParam reads ?snapshot=<id> from the request:
//   - absent → returns (0, true): caller serves latest as before.
//   - non-integer, ≤0, or unknown for this city → writes 404 and returns
//     (_, false); the bead spec wants invalid ids to 404, not 500.
//   - valid id belonging to this city → returns (id, true).
func (s *Server) parseSnapshotParam(ctx context.Context, w http.ResponseWriter, r *http.Request, store db.Store) (int64, bool) {
	raw := r.URL.Query().Get("snapshot")
	if raw == "" {
		return 0, true
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return 0, false
	}
	if err := store.ResolveSnapshot(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return 0, false
		}
		s.httpErr(w, err, http.StatusInternalServerError)
		return 0, false
	}
	return id, true
}

// snapshotMatchesConfig verifies that an explicitly pinned snapshot was
// computed under the SAME config hash as the one currently serving. On a
// mismatch it writes 409 Conflict (the pinned snapshot exists but conflicts
// with the live grid) and returns false; the caller must not proceed. On a
// match (or when no config is attached, e.g. some tests) it returns true.
//
// The single-snapshot lookup filters entry.Store.ListSnapshots by id rather
// than adding a new Store accessor: ListSnapshots is already on the interface
// (every mock implements it) and a city's snapshot list is small, so filtering
// is KISS and avoids rippling a GetSnapshot method through db.Store and every
// mock. parseSnapshotParam has already confirmed the id resolves for this city,
// so a missing row here would be a race, not bad input.
func (s *Server) snapshotMatchesConfig(ctx context.Context, w http.ResponseWriter, entry export.CityEntry, snapshotID int64) bool {
	if entry.Config == nil {
		return true
	}
	want := entry.Config.Hash()
	snaps, err := entry.Store.ListSnapshots(ctx)
	if err != nil {
		s.httpErr(w, fmt.Errorf("listing snapshots: %w", err), http.StatusInternalServerError)
		return false
	}
	for _, snap := range snaps {
		if snap.ID != snapshotID {
			continue
		}
		if snap.ConfigHash == want {
			return true
		}
		fmt.Fprintf(s.ios.ErrOut,
			"server: 409 Conflict: snapshot %d config_hash %q != current %q\n",
			snapshotID, snap.ConfigHash, want)
		http.Error(w, fmt.Sprintf(
			"snapshot %d was computed under a different config (hex_edge_m or other grid "+
				"setting changed) and cannot be served against the current hex grid; "+
				"recompute with the current config or pick a snapshot matching it",
			snapshotID), http.StatusConflict)
		return false
	}
	// Resolved by parseSnapshotParam but absent from the list: treat as a
	// transient inconsistency rather than silently serving mismatched data.
	s.httpErr(w, fmt.Errorf("snapshot %d resolved but not found in snapshot list", snapshotID), http.StatusInternalServerError)
	return false
}

// cacheKey composes the s.cache key for a per-snapshot JSON build. The
// trailing :%d keeps "latest" (snapshotID=0) separate from any pinned
// snapshot, and two pinned snapshots cache independently.
func cacheKey(kind, slug string, snapshotID int64) string {
	return fmt.Sprintf("%s:%s:%d", kind, slug, snapshotID)
}

// writeJSON encodes v as JSON and writes it to w with appropriate headers.
func (s *Server) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.httpErr(w, err, http.StatusInternalServerError)
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
		s.httpErr(w, err, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Write(data)
}

func (s *Server) serveMetaJSON(w http.ResponseWriter, _ *http.Request, entry export.CityEntry, snapshotID int64) {
	s.serveJSONCached(w, cacheKey("meta", entry.Slug, snapshotID), func() (any, error) {
		return export.BuildMeta(context.Background(), entry)
	})
}

// serveHexGridGeoJSON serves the single multi-scope hex grid at
// /data/hexgrid.geojson — one feature per hex with nested {bbox, city?}
// coverage. A city with no rows returns an empty FeatureCollection; features
// without a "city" object tell the client to hide the scope toggle.
func (s *Server) serveHexGridGeoJSON(w http.ResponseWriter, _ *http.Request, entry export.CityEntry, snapshotID int64) {
	s.serveJSONCached(w, cacheKey("hexgrid", entry.Slug, snapshotID), func() (any, error) {
		_, lon0, lat0, err := entry.BBoxAndCenter(context.Background())
		if err != nil {
			return nil, err
		}
		// BuildHexGeoJSON distinguishes "no hex stats" (nil, nil — cache the
		// empty FC) from a real DB error (nil, err — surface so serveJSONCached
		// evicts and the next request retries instead of locking in a blank hex
		// grid for the server's lifetime), mirroring serveBoundaryGeoJSON.
		fc, err := export.BuildHexGeoJSON(context.Background(), entry, geo.NewUTMProjector(lon0, lat0))
		if err != nil {
			return nil, err
		}
		if fc == nil {
			fc = map[string]any{"type": "FeatureCollection", "features": []any{}}
		}
		return fc, nil
	})
}

func (s *Server) serveScenariosJSON(w http.ResponseWriter, _ *http.Request, entry export.CityEntry, snapshotID int64) {
	s.serveJSONCached(w, cacheKey("scenarios", entry.Slug, snapshotID), func() (any, error) {
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
func (s *Server) buildForecasts(entry export.CityEntry, snapshotID int64) ([]export.ForecastExport, error) {
	key := fmt.Sprintf("%s:%d", entry.Slug, snapshotID)
	var ft *forecastThunk
	if v, ok := s.forecasts.Load(key); ok {
		ft = v.(*forecastThunk) //nolint:forcetypeassert // type invariant: only this site Stores into s.forecasts
	} else {
		fresh := &forecastThunk{once: sync.OnceValues(func() ([]export.ForecastExport, error) {
			fc := entry.Config.ResolvedForecast(&entry.City)
			return export.BuildForecastsForCity(context.Background(), entry, &fc, export.ConvertCostTiers(&fc))
		})}
		actual, _ := s.forecasts.LoadOrStore(key, fresh)
		ft = actual.(*forecastThunk) //nolint:forcetypeassert // type invariant: only this site Stores into s.forecasts
	}

	// Match serveJSONCached's panic-evict semantics so a panicking forecast
	// build doesn't permanently poison the city's forecast cache.
	defer func() {
		if r := recover(); r != nil {
			s.forecasts.CompareAndDelete(key, ft)
			panic(r)
		}
	}()
	forecasts, err := ft.once()
	if err != nil {
		s.forecasts.CompareAndDelete(key, ft)
		return nil, err
	}
	return forecasts, nil
}

func (s *Server) serveForecastJSON(w http.ResponseWriter, _ *http.Request, entry export.CityEntry, snapshotID int64) {
	s.serveJSONCached(w, cacheKey("forecast", entry.Slug, snapshotID), func() (any, error) {
		return s.buildForecasts(entry, snapshotID)
	})
}

func (s *Server) serveHexCostSummary(w http.ResponseWriter, _ *http.Request, entry export.CityEntry, snapshotID int64) {
	s.serveJSONCached(w, cacheKey("hexcost", entry.Slug, snapshotID), func() (any, error) {
		forecasts, err := s.buildForecasts(entry, snapshotID)
		if err != nil {
			return nil, err
		}
		return export.BuildHexCostSummary(context.Background(), entry, forecasts), nil
	})
}

func (s *Server) serveBoundaryGeoJSON(w http.ResponseWriter, _ *http.Request, entry export.CityEntry, snapshotID int64) {
	s.serveJSONCached(w, cacheKey("boundary", entry.Slug, snapshotID), func() (any, error) {
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

func (s *Server) serveForecastSeed(w http.ResponseWriter, _ *http.Request, entry export.CityEntry, snapshotID int64) {
	s.serveJSONCached(w, cacheKey("seed", entry.Slug, snapshotID), func() (any, error) {
		fc := entry.Config.ResolvedForecast(&entry.City)
		seed, err := export.BuildForecastSeed(context.Background(), &fc, entry.Store)
		if err != nil {
			return nil, err
		}
		return json.RawMessage(seed), nil
	})
}

// serveSnapshotsJSON serves the per-city snapshot list. Snapshots are
// append-only at the data layer, so the cache is never invalidated —
// new snapshots written while the server is running won't appear until
// restart. Acceptable for the time-travel UI, which targets historic data.
func (s *Server) serveSnapshotsJSON(w http.ResponseWriter, _ *http.Request, entry export.CityEntry) {
	s.serveJSONCached(w, "snapshots:"+entry.Slug, func() (any, error) {
		snaps, err := entry.Store.ListSnapshots(context.Background())
		if err != nil {
			return nil, fmt.Errorf("listing snapshots: %w", err)
		}
		if snaps == nil {
			snaps = []db.Snapshot{}
		}
		return snaps, nil
	})
}
