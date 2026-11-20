package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/warn"
)

// handleIndex serves the rendered index page from the lifetime cache. The
// first request builds it (choose city, build TemplateData, parse template,
// render to bytes); every later request writes the cached bytes. See
// renderIndex for the single-flight/caching mechanism and why HTML can't ride
// serveJSONCached.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	if len(s.cities) == 0 {
		http.Error(w, "no cities configured", http.StatusInternalServerError)
		return
	}

	page, err := s.renderIndex()
	if err != nil {
		s.httpErr(w, err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Write(page)
}

// indexThunk wraps a sync.OnceValues thunk so s.indexPages stores
// pointer-comparable values (see jsonThunk).
type indexThunk struct {
	once func() ([]byte, error)
}

// renderIndex returns the rendered index HTML, building it at most once for
// the server's lifetime. The render pipeline — firstRenderableCity (which
// picks the first city whose buildIndexData succeeds), s.indexTemplate (the
// lifetime-cached parse), and tmpl.Execute into a buffer — is deterministic
// under the restart-after-changes invariant: cities iterate in fixed order and
// the data is immutable, so the first successful render is the canonical page.
//
// HTML can't ride serveJSONCached (that marshals JSON and sets
// application/json), so this uses a parallel sync.OnceValues against
// s.indexPages. A single fixed key suffices because the chosen slug is
// deterministic; storing under the chosen slug after the fact would still
// collapse to one entry. Errors and panics evict so the next request retries
// rather than serving a stale failure — only a successful render is cached, so
// a broken first city still falls through to a healthy one (firstRenderableCity
// handles that), and the all-cities-broken path returns the error to handleIndex
// for s.httpErr.
//
// BuildMeta's SnapshotDate is the date of the data (the snapshot's, or the
// latest compute's newest run), so the cached page shows when the city was
// computed, whenever it is first requested.
func (s *Server) renderIndex() ([]byte, error) {
	if len(s.cities) > 1 {
		return s.renderCachedPage("index", &s.indexPages, s.indexTemplate, s.buildSiteIndexData)
	}
	return s.renderCachedPage("index", &s.indexPages, s.indexTemplate, s.firstRenderableCity)
}

// renderCachedPage is the shared engine behind renderIndex and renderGame: it
// renders the chosen template against the resolver's TemplateData and caches the
// resulting bytes under key in pages, single-flighting via sync.OnceValues and
// evicting on error/panic so a failed render isn't locked in for the server's
// lifetime. pages, parse, and resolve are the only things that differ between
// the index and game pages.
//
// resolve picks the city (and its TemplateData) the page renders against; it is
// the only thing that differs between the index (always firstRenderableCity)
// and the game's per-?city= render (a chosen entry with firstRenderableCity
// fallback). It runs against context.Background() — the first arriver's build
// outlives their request and is shared.
func (s *Server) renderCachedPage(key string, pages *sync.Map, parse func() (*template.Template, error), resolve func(context.Context) (export.CityEntry, export.TemplateData, error)) ([]byte, error) {
	var entry *indexThunk
	if v, ok := pages.Load(key); ok {
		entry = v.(*indexThunk) //nolint:forcetypeassert // type invariant: only render sites Store *indexThunk here
	} else {
		fresh := &indexThunk{once: sync.OnceValues(func() ([]byte, error) {
			_, td, err := resolve(context.Background())
			if err != nil {
				return nil, err
			}
			return export.RenderPage(parse, td)
		})}
		actual, _ := pages.LoadOrStore(key, fresh)
		entry = actual.(*indexThunk) //nolint:forcetypeassert // type invariant: only render sites Store *indexThunk here
	}

	// Evict on panic and re-raise (see serveJSONCached) so a panicking render
	// doesn't poison the key forever.
	defer func() {
		if r := recover(); r != nil {
			pages.CompareAndDelete(key, entry)
			panic(r)
		}
	}()

	page, err := entry.once()
	if err != nil {
		pages.CompareAndDelete(key, entry)
		return nil, err
	}
	return page, nil
}

// handleGame serves the /play game page. Unlike handleIndex (which renders one
// canonical page and switches cities client-side via DATA_PREFIX), the game
// renders per-city: the page injects a city's FORECAST_SEED, CENTER, and project
// name as scalars used to *seed* the simulation, so each city must be rendered
// against its own TemplateData. The selected city comes from ?city=<slug>; the
// dropdown navigates there. It rides the same lifetime-cache + shared
// /pvmt.wasm + per-city /data endpoints — only the template differs.
func (s *Server) handleGame(w http.ResponseWriter, r *http.Request) {
	if len(s.cities) == 0 {
		http.Error(w, "no cities configured", http.StatusInternalServerError)
		return
	}

	page, err := s.renderGame(r.URL.Query().Get("city"))
	if err != nil {
		s.httpErr(w, err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Write(page) //nolint:gosec // G705: page is html/template output (auto-escaped); ?city only selects a pre-configured city via cityBySlug, it is never written into the page.
}

// renderGame renders (and caches per slug) the /play page for the requested
// city. A known slug renders that city; an empty or unknown slug — and any city
// whose data fails to build — falls back to defaultGameCity, preserving the
// skip-broken-city tolerance of the rest of the multi-city surface. Each city's
// page is cached under its own key so a busy city doesn't re-render per request.
//
// The per-slug key ("game:"+slug) caches ONLY a successful render of THAT city.
// If the requested city's own build fails, we render the fallback under the
// shared "game" key instead — never under "game:"+slug. Otherwise a transient
// DB error at first render would bake the fallback city's HTML (with the wrong
// city's DATA_PREFIX/FORECAST_SEED) into the requested slug's lifetime cache,
// pinning city A's /play page to city B until restart. Routing the fallback to
// the shared key leaves the requested slug's key empty, so once its store
// recovers the next request re-attempts (and caches) the correct city.
func (s *Server) renderGame(slug string) ([]byte, error) {
	entry := s.cityBySlug(slug)
	if entry == nil {
		// Empty or unknown slug: render the default city under the shared key
		// (there is no per-slug identity to cache against).
		return s.renderCachedPage("game", &s.gamePages, s.gameTemplate, s.defaultGameCity)
	}

	// Try the requested city under its own key. resolve returns the requested
	// city's build error (no in-closure fallback) so a failed build evicts the
	// per-slug entry rather than caching a fallback under it.
	page, err := s.renderCachedPage("game:"+slug, &s.gamePages, s.gameTemplate,
		func(ctx context.Context) (export.CityEntry, export.TemplateData, error) {
			td, err := s.buildIndexData(ctx, *entry)
			if err != nil {
				return export.CityEntry{}, export.TemplateData{}, err
			}
			return *entry, td, nil
		})
	if err == nil {
		return page, nil
	}

	// The requested city didn't build. Don't 500 the game — fall back to the
	// default city, cached under the shared "game" key (never the requested
	// slug's key).
	fmt.Fprintf(s.ios.ErrOut, "server: falling back for /play?city=%s: %v\n", slug, err)
	return s.renderCachedPage("game", &s.gamePages, s.gameTemplate, s.defaultGameCity)
}

// defaultGameCity is the city /play opens on when the request names none: the
// one a static export's play.html opens on (export.DefaultCitySlug, the city
// selector's first option). A city that fails to build, or a list that cannot
// be read, falls back to firstRenderableCity.
func (s *Server) defaultGameCity(ctx context.Context) (export.CityEntry, export.TemplateData, error) {
	if cities, err := s.listedCities(ctx); err == nil {
		if entry := s.cityBySlug(export.DefaultCitySlug(cities)); entry != nil {
			if td, err := s.buildIndexData(ctx, *entry); err == nil {
				return *entry, td, nil
			}
		}
	}
	return s.firstRenderableCity(ctx)
}

// firstRenderableCity returns the first city entry whose buildIndexData
// succeeds, along with its assembled TemplateData. Returns the last build error only when no city renders, so a
// single broken city does not 500 the entire site.
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

// buildIndexData assembles the TemplateData for one city's pages: the /play
// page of any city, and the index of a single-city server. The cities list is
// populated only when len(s.cities) > 1 so the single-city DATA_PREFIX wiring
// keeps matching the /data/{file} routes.
func (s *Server) buildIndexData(ctx context.Context, entry export.CityEntry) (export.TemplateData, error) {
	// The pages always render the pinned view; other snapshots, and the
	// latest compute, are served via /data/meta.json?snapshot=<name>|latest
	// (serveMetaJSON). The pinned view's drawn grid is the one its
	// /data/meta.json, hexgrid.geojson and hex-metrics.json share.
	type page struct {
		meta export.MetaJSON
		seed template.JS
	}
	built, err := s.withDrawnGrid(entry, 0, func(grid *export.DrawnGrid) (any, error) {
		meta, seed, err := export.CityPageMetaAndSeed(ctx, entry.Config, entry, grid)
		return page{meta, seed}, err
	})
	if err != nil {
		return export.TemplateData{}, err
	}
	meta, seed := built.(page).meta, built.(page).seed //nolint:forcetypeassert // the build above returns a page
	var cities []export.CityInfo
	var activeSlug string
	if len(s.cities) > 1 {
		activeSlug = entry.Slug
		if cities, err = s.listedCities(ctx); err != nil {
			return export.TemplateData{}, err
		}
	}
	return s.templateData(ctx, entry.Config, meta, seed, cities, activeSlug)
}

// buildSiteIndexData assembles the TemplateData for a multi-city index: the
// config-wide meta and forecast seed a static export's index.html carries,
// rather than any one city's. The index switches cities client-side, so it
// has no active city.
func (s *Server) buildSiteIndexData(ctx context.Context) (export.CityEntry, export.TemplateData, error) {
	cfg := s.cities[0].Config
	title := "pvmt"
	if cfg.SourcePath != "" {
		title = filepath.Base(filepath.Dir(cfg.SourcePath))
	}
	meta, seed, err := export.PageMetaAndSeed(ctx, cfg, s.cities, export.SiteTitle(cfg, title))
	if err != nil {
		return export.CityEntry{}, export.TemplateData{}, err
	}
	cities, err := s.listedCities(ctx)
	if err != nil {
		return export.CityEntry{}, export.TemplateData{}, err
	}
	td, err := s.templateData(ctx, cfg, meta, seed, cities, "")
	return export.CityEntry{}, td, err
}

// listedCities is the page's CITIES array. export.CityInfos leaves out a city
// with no boundary ingested (logged, since the rendered HTML is
// lifetime-cached) and returns any other error, so renderCachedPage evicts and
// the next request retries rather than baking a missing city into the page.
func (s *Server) listedCities(ctx context.Context) ([]export.CityInfo, error) {
	return export.CityInfos(ctx, s.cities, s.logUnlisted("the index dropdown"))
}

// templateData is export.NewTemplateData marked as a live-server render. The
// Config tab shows each city's pinned snapshot's hex edge.
func (s *Server) templateData(ctx context.Context, cfg *config.Config, meta export.MetaJSON, seed template.JS, cities []export.CityInfo, activeSlug string) (export.TemplateData, error) {
	hexEdges, err := export.HexEdges(ctx, s.cities)
	if err != nil {
		return export.TemplateData{}, err
	}
	td, err := export.NewTemplateData(cfg, meta, seed, cities, s.unitSystem(cfg), hexEdges)
	if err != nil {
		return export.TemplateData{}, err
	}
	td.ActiveSlug = activeSlug
	td.IsLiveServer = true
	return td, nil
}

// unitSystem is the display unit system the pages open in: s.UnitSystem when
// the caller set one (pvmt serve passes --units), else the config's.
func (s *Server) unitSystem(cfg *config.Config) string {
	if s.UnitSystem != "" {
		return s.UnitSystem
	}
	return cfg.UnitSystem().String()
}

// httpErr logs the full err server-side and writes a generic status-matched
// message to the client. The split exists so DB paths, file paths, and
// wrapped error chains stay out of the response body while operators still
// see the real cause in the server log.
//
// db.ErrSnapshotGone overrides code: the snapshot this server pinned at
// startup (or a ?snapshot=<name> that resolved a moment ago) was deleted or
// pruned, which a retry cannot fix, so it is 410 Gone with a fixed message
// saying to restart. Every cache here evicts errors, so the 410 is never
// memoized and a restarted server serves the current snapshot.
//
//nolint:unparam // every current caller is 500, but the helper is the seam for any 4xx/5xx surface.
func (s *Server) httpErr(w http.ResponseWriter, err error, code int) {
	msg := http.StatusText(code)
	if errors.Is(err, db.ErrSnapshotGone) {
		code, msg = http.StatusGone, snapshotGoneMessage
	}
	fmt.Fprintf(s.ios.ErrOut, "server: %d %s: %v\n", code, http.StatusText(code), err)
	http.Error(w, msg, code)
}

// snapshotGoneMessage is the 410 body for a read of a deleted snapshot.
const snapshotGoneMessage = "the snapshot this page was serving has been deleted or pruned; " +
	"restart 'pvmt serve' to serve the current snapshot"

// handleAsset serves one site-root browser asset. They are immutable for the
// server's lifetime, so they get the same Cache-Control as the JSON endpoints
// (serveJSONCached): the browser stops re-fetching them, the ~3.4 MB
// pvmt.wasm above all, under the restart-after-changes invariant.
func (s *Server) handleAsset(name string) http.HandlerFunc {
	contentType := "application/javascript"
	if strings.HasSuffix(name, ".wasm") {
		contentType = "application/wasm"
	}
	body := s.assets.Bytes(name)
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Write(body)
	}
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

// handleCitiesList returns JSON list of all cities. The list is immutable for
// the server's lifetime (same restart-after-changes invariant as the /data
// endpoints), and e.Info does a GetBoundary + full-coordinate BBoxFromGeoJSON
// walk per city, so it routes through serveJSONCached for single-flight,
// lifetime caching, and the Cache-Control header. The build runs against
// context.Background() (not r.Context()) for the reason documented on
// serveJSONCached: the first arriver's build outlives their request.
func (s *Server) handleCitiesList(w http.ResponseWriter, _ *http.Request) {
	s.serveJSONCached(w, "cities", func() (any, error) {
		// The same list, order and skip rule as the static cities.json. An
		// error other than a missing boundary fails the request, and
		// serveJSONCached evicts so the next one retries.
		return export.CityInfos(context.Background(), s.cities, s.logUnlisted("/api/cities"))
	})
}

// logUnlisted returns export.CityInfos' callback: it logs each city left out
// of a lifetime-cached list for want of a boundary, so the drop is not silent.
func (s *Server) logUnlisted(where string) func(export.CityEntry) {
	return func(e export.CityEntry) {
		warn.Fprintf(s.ios.ErrOut, "skipping %s in %s: no boundary stored", e.Slug, where)
	}
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
	// Any snapshot renders on its own lattice: a pinned store reads the
	// snapshot's hex edge and frozen boundary (CityEntry.HexEdge,
	// GetBoundary), so a snapshot computed under other settings, at another
	// hex_edge_m say, draws its stored hex ids on the hexes they were
	// computed for.
	switch {
	case snapshotID == latestSnapshot:
		entry = export.LatestComputeEntry(entry, entry.Latest)
	case snapshotID > 0:
		entry = entry.WithSnapshot(snapshotID)
	}
	serve, ok := s.dataFiles()[file]
	if !ok {
		http.NotFound(w, r)
		return
	}
	serve(w, r, entry, snapshotID)
}

// dataFileHandler serves one of a city's data files.
type dataFileHandler func(w http.ResponseWriter, r *http.Request, entry export.CityEntry, snapshotID int64)

// dataFiles routes every per-city data file a static export can write
// (export.AllDataFileNames) to the handler that builds the same document.
func (s *Server) dataFiles() map[string]dataFileHandler {
	return map[string]dataFileHandler{
		export.MetaFileName:           s.serveMetaJSON,
		export.HexGridFileName:        s.serveHexGridGeoJSON,
		export.ScenariosFileName:      s.serveScenariosJSON,
		export.ForecastFileName:       s.serveForecastJSON,
		export.ForecastSeedFileName:   s.serveForecastSeed,
		export.HexCostSummaryFileName: s.serveHexCostSummary,
		export.BoundaryFileName:       s.serveBoundaryGeoJSON,
		export.HexMetricsFileName:     s.serveHexMetrics,
	}
}

// latestSnapshot is the snapshot id parseSnapshotParam returns for
// ?snapshot=latest: the city's latest compute as the server read it at
// startup (CityEntry.Latest), snapshotted or not. Negative, so it never
// collides with a snapshot id and caches under its own key.
const latestSnapshot int64 = -1

// latestSnapshotParam is the ?snapshot= value for latestSnapshot. No snapshot
// can be named it (db.CheckSnapshotName).
const latestSnapshotParam = db.LatestComputeName

// parseSnapshotParam reads ?snapshot=<name> from the request:
//   - absent → returns (0, true): caller serves the pinned view.
//   - "latest" → returns (latestSnapshot, true).
//   - an invalid name, or a snapshot that does not cover this city → writes
//     404 and returns (_, false): an unknown snapshot is a 404, not a 500.
//   - a snapshot covering this city → returns (its id, true). Responses
//     cache under the id, so a snapshot deleted and created again under the
//     same name never serves the old one's cache.
func (s *Server) parseSnapshotParam(ctx context.Context, w http.ResponseWriter, r *http.Request, store db.Store) (int64, bool) {
	raw := r.URL.Query().Get("snapshot")
	if raw == "" {
		return 0, true
	}
	if raw == latestSnapshotParam {
		return latestSnapshot, true
	}
	if db.CheckSnapshotName(raw) != nil {
		http.NotFound(w, r)
		return 0, false
	}
	id, err := store.ResolveSnapshot(ctx, raw)
	if err != nil {
		if errors.Is(err, db.ErrSnapshotNotFound) {
			http.NotFound(w, r)
			return 0, false
		}
		s.httpErr(w, err, http.StatusInternalServerError)
		return 0, false
	}
	return id, true
}

// cacheKey composes the s.cache key for a per-snapshot JSON build. The
// trailing :%d keeps "latest" (snapshotID=0) separate from any pinned
// snapshot, and two pinned snapshots cache independently.
func cacheKey(kind, slug string, snapshotID int64) string {
	return fmt.Sprintf("%s:%s:%d", kind, slug, snapshotID)
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

// withDrawnGrid runs build on the city's drawn hex grid for snapshotID
// (export.DrawnGrid), shared by meta.json, hex-metrics.json, hexgrid.geojson
// and the pages, so a cold city clips its lattice once however many of them
// arrive together, rather than once each. entry is the entry serveDataFile
// resolved for snapshotID, so one key is always one snapshot, boundary and
// hex edge.
//
// A DrawnGrid keeps its first build's result, error included, so a build
// over it that fails (or panics) drops it from s.grids: the next request
// builds a fresh one, matching the evict-on-error rule of every cache here.
// Like serveJSONCached builds, the grid runs under context.Background(): the
// first arriver's build is shared and outlives their request.
func (s *Server) withDrawnGrid(entry export.CityEntry, snapshotID int64, build func(*export.DrawnGrid) (any, error)) (any, error) {
	key := cacheKey("grid", entry.Slug, snapshotID)
	var grid *export.DrawnGrid
	if v, ok := s.grids.Load(key); ok {
		grid = v.(*export.DrawnGrid) //nolint:forcetypeassert // type invariant: only this site Stores into s.grids
	} else {
		actual, _ := s.grids.LoadOrStore(key, export.NewDrawnGrid(context.Background(), entry, nil))
		grid = actual.(*export.DrawnGrid) //nolint:forcetypeassert // type invariant: only this site Stores into s.grids
	}
	defer func() {
		if r := recover(); r != nil {
			s.grids.CompareAndDelete(key, grid)
			panic(r)
		}
	}()
	v, err := build(grid)
	if err != nil {
		s.grids.CompareAndDelete(key, grid)
		return nil, err
	}
	return v, nil
}

func (s *Server) serveMetaJSON(w http.ResponseWriter, _ *http.Request, entry export.CityEntry, snapshotID int64) {
	s.serveJSONCached(w, cacheKey("meta", entry.Slug, snapshotID), func() (any, error) {
		return s.withDrawnGrid(entry, snapshotID, func(grid *export.DrawnGrid) (any, error) {
			return export.BuildMetaOn(context.Background(), entry, snapshotID, grid)
		})
	})
}

// serveHexGridGeoJSON serves the single multi-scope hex grid at
// /data/hexgrid.geojson — one feature per hex with nested
// {all, city|city_same} coverage. A city with no rows returns an empty
// FeatureCollection; features with NEITHER city key tell the client to hide the
// scope toggle.
func (s *Server) serveHexGridGeoJSON(w http.ResponseWriter, _ *http.Request, entry export.CityEntry, snapshotID int64) {
	s.serveJSONCached(w, cacheKey("hexgrid", entry.Slug, snapshotID), func() (any, error) {
		return s.withDrawnGrid(entry, snapshotID, func(grid *export.DrawnGrid) (any, error) {
			// BuildHexGeoJSONOn distinguishes "no hex stats" (nil, nil — cache
			// the empty FC) from a real DB error (nil, err — surface so
			// serveJSONCached evicts and the next request retries instead of
			// locking in a blank hex grid for the server's lifetime), mirroring
			// serveBoundaryGeoJSON. The grid derives its projector from the
			// boundary, as the static export does.
			fc, err := export.BuildHexGeoJSONOn(context.Background(), entry, grid)
			if err != nil {
				return nil, err
			}
			if fc == nil {
				fc = export.EmptyHexGrid()
			}
			return fc, nil
		})
	})
}

// serveHexMetrics serves the optional per-hex metrics file. A city with no
// metric rows gets an empty, versioned document (the static export writes no
// file and the client treats a 404 the same way); a real store error
// propagates so serveJSONCached evicts and retries.
func (s *Server) serveHexMetrics(w http.ResponseWriter, _ *http.Request, entry export.CityEntry, snapshotID int64) {
	s.serveJSONCached(w, cacheKey("hexmetrics", entry.Slug, snapshotID), func() (any, error) {
		return s.withDrawnGrid(entry, snapshotID, func(grid *export.DrawnGrid) (any, error) {
			hm, err := export.BuildHexMetricsOn(context.Background(), entry, grid)
			if err != nil {
				return nil, err
			}
			if hm == nil {
				return export.EmptyHexMetrics(), nil
			}
			return hm, nil
		})
	})
}

func (s *Server) serveScenariosJSON(w http.ResponseWriter, _ *http.Request, entry export.CityEntry, snapshotID int64) {
	s.serveJSONCached(w, cacheKey("scenarios", entry.Slug, snapshotID), func() (any, error) {
		fc := entry.Config.ResolvedForecast(&entry.City)
		// BuildScenariosData returns real DB errors, so serveJSONCached evicts
		// and the next request retries instead of locking in a zero-area
		// payload for the server's lifetime; nothing computed is just empty.
		data, err := export.BuildScenariosData(context.Background(), entry, &fc)
		if err != nil {
			return nil, err
		}
		// Same emission-time rounding the static export applies, so served and
		// exported bytes stay identical.
		return export.RoundScenariosForEmission(data), nil
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
		forecasts, err := s.buildForecasts(entry, snapshotID)
		if err != nil {
			return nil, err
		}
		// Round for emission only, and on a copy — buildForecasts memoizes this
		// slice and serveHexCostSummary derives from the same cached value, so
		// rounding in place would both mutate shared state and feed the hex cost
		// summary rounded input. Mirrors exportScenariosForCity.
		return export.RoundForecastsForEmission(forecasts), nil
	})
}

func (s *Server) serveHexCostSummary(w http.ResponseWriter, _ *http.Request, entry export.CityEntry, snapshotID int64) {
	s.serveJSONCached(w, cacheKey("hexcost", entry.Slug, snapshotID), func() (any, error) {
		forecasts, err := s.buildForecasts(entry, snapshotID)
		if err != nil {
			return nil, err
		}
		return export.BuildHexCostSummary(forecasts), nil
	})
}

// serveBoundaryGeoJSON serves the display boundary, built through the same
// export.BuildBoundaryGeoJSON the static exporter uses so the served bytes and
// the exported file stay identical.
//
// The cache key is the boundary itself (the frozen boundary's digest), not
// the snapshot: snapshots that froze the same boundary share one entry, so
// scrubbing the time-travel picker does not re-run the projector derivation —
// which json.Unmarshals once per coordinate, 205,961 times for Jacksonville —
// to produce the same bytes, while a snapshot clipped to another boundary
// gets its own.
func (s *Server) serveBoundaryGeoJSON(w http.ResponseWriter, _ *http.Request, entry export.CityEntry, _ int64) {
	key, err := boundaryCacheKey(entry)
	if err != nil {
		s.httpErr(w, err, http.StatusInternalServerError)
		return
	}
	s.serveJSONCached(w, key, func() (any, error) {
		ctx := context.Background()

		// Derive the projector the shared builder needs. BBoxAndCenter reports
		// a genuinely-absent boundary as ErrNoBoundary (cache the empty FC —
		// the city is simply unconfigured) and propagates real DB errors so
		// serveJSONCached evicts and the next request retries instead of
		// locking in an empty boundary for the server's lifetime.
		//
		// Anything else it can fail on — a stored Feature wrapper, a
		// GeometryCollection, an out-of-range or antimeridian-spanning polygon
		// — is NOT a reason to fail the request. All of those serve 200 today.
		// Fall through with a nil projector, which makes BuildBoundaryGeoJSON
		// skip simplification and emit the stored geometry as-is. Returning the
		// error instead would evict the cache entry and 500 on every request,
		// forever, for a city that renders fine.
		var proj *geo.UTMProjector
		_, lon, lat, err := entry.BBoxAndCenter(ctx)
		switch {
		case errors.Is(err, export.ErrNoBoundary):
			return export.EmptyBoundary(), nil
		case err != nil:
			fmt.Fprintf(s.ios.ErrOut,
				"server: city %q boundary has no derivable projection (%v); serving it unsimplified\n",
				entry.City.Name, err)
		default:
			proj = geo.NewUTMProjector(lon, lat)
		}

		fc, simplifyErr := export.BuildBoundaryGeoJSON(ctx, entry, proj)
		if fc == nil {
			// Either a GetBoundary failure (propagate: evict and retry) or the
			// boundary vanished between the two reads (empty FC).
			if simplifyErr != nil {
				return nil, simplifyErr
			}
			return export.EmptyBoundary(), nil
		}
		if simplifyErr != nil {
			fmt.Fprintf(s.ios.ErrOut,
				"server: city %q boundary could not be simplified, serving it in full: %v\n",
				entry.City.Name, simplifyErr)
		}
		return fc, nil
	})
}

// boundaryCacheKey keys a city's boundary.geojson on the boundary its store
// reads: the frozen boundary's digest for a snapshot that froze one, else
// "live" (the stored boundary, cached for the server's lifetime like every
// other endpoint).
func boundaryCacheKey(entry export.CityEntry) (string, error) {
	snap, err := entry.Store.ViewSnapshot(context.Background())
	if err != nil {
		return "", err
	}
	id := "live"
	if snap != nil && snap.BoundaryDigest != "" {
		id = snap.BoundaryDigest
	}
	return "boundary:" + entry.Slug + ":" + id, nil
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

// snapshotJSON is one entry of /api/snapshots: the city in a snapshot, and
// whether it is the one this server pinned at startup (what the picker's
// default option shows).
type snapshotJSON struct {
	db.SnapshotCity
	Served bool `json:"served,omitempty"`
}

// serveSnapshotsJSON serves the per-city snapshot list, newest first: the one
// read at startup with the pin (CityEntry.Snapshots) when there is one, with
// the pinned snapshot marked served. New snapshots appear after a restart, as
// new data does everywhere else in serve.
func (s *Server) serveSnapshotsJSON(w http.ResponseWriter, _ *http.Request, entry export.CityEntry) {
	s.serveJSONCached(w, "snapshots:"+entry.Slug, func() (any, error) {
		ctx := context.Background()
		snaps := entry.Snapshots
		if snaps == nil {
			var err error
			if snaps, err = entry.Store.ListSnapshots(ctx); err != nil {
				return nil, fmt.Errorf("listing snapshots: %w", err)
			}
		}
		var served int64
		if !entry.ReadsLatest() {
			pinned, err := entry.Store.ViewSnapshot(ctx)
			if err != nil {
				return nil, err
			}
			if pinned != nil {
				served = pinned.ID
			}
		}
		out := make([]snapshotJSON, len(snaps))
		for i, snap := range snaps {
			out[i] = snapshotJSON{SnapshotCity: snap, Served: served != 0 && snap.ID == served}
		}
		return out, nil
	})
}
