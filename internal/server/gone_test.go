package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

const goneBoundary = `{"type":"Polygon","coordinates":[[[-121.84,37.64],[-121.68,37.64],[-121.68,37.72],[-121.84,37.72],[-121.84,37.64]]]}`

// cityDB is a city's store with the root it came from, for creating and
// deleting snapshots, which are root-level.
type cityDB struct {
	db.Store
	root db.RootStorer
	id   int64
}

// sqliteEntry returns a city in a real in-memory database with goneBoundary
// stored, scoped to its current hash as BuildCityEntries makes it, and a
// commit func that adds a roads snapshot and returns its id.
func sqliteEntry(t *testing.T, cfg *config.Config, city config.CityConfig) (export.CityEntry, *cityDB, func(area float64) int64) {
	t.Helper()
	ctx := context.Background()
	root, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	id, err := root.EnsureCity(ctx, city.Slug(), city.Name, "test")
	if err != nil {
		t.Fatal(err)
	}
	store := &cityDB{Store: root.ForCity(id), root: root, id: id}
	if err := store.SaveBoundary(ctx, goneBoundary, "test"); err != nil {
		t.Fatal(err)
	}
	commit := func(area float64) int64 {
		t.Helper()
		hash := cfg.CityHash(&city)
		run, err := store.CreateRun(ctx, "roads", hash, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveCityMetrics(ctx, run.ID, results.TotalRows(resource.TypeRoads, results.Totals{Area: area, Features: 1})); err != nil {
			t.Fatal(err)
		}
		snap, err := dbtest.Publish(ctx, store.root, store.id, hash, cfg.ResolvedHexEdge(&city), map[string]int64{"roads": run.ID})
		if err != nil {
			t.Fatal(err)
		}
		return snap.ID
	}
	entry := export.CityEntry{Config: cfg, City: city, Store: store.WithLatestCompute(cfg.CityHash(&city)), Slug: city.Slug()}
	return entry, store, commit
}

// TestDataFile_PinnedSnapshotDeletedIs410: when the snapshot serve pinned at
// startup is deleted, every uncached data file answers 410 Gone saying to
// restart, not 200 with an empty city, and the failure is not cached.
func TestDataFile_PinnedSnapshotDeletedIs410(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{Cities: []config.CityConfig{{Name: "Test City"}}}
	entry, store, commit := sqliteEntry(t, cfg, cfg.Cities[0])
	commit(100)
	newest := commit(200)
	pinned, err := entry.PinNewestSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}

	ios, _, _, _ := iostreams.Test()
	srv := New([]export.CityEntry{pinned}, "127.0.0.1", 0, ios, testAssets(t))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /data/{file}", srv.handleDataFile(pinned))
	hit := func(file string) *httptest.ResponseRecorder {
		req, _ := http.NewRequestWithContext(ctx, "GET", "/data/"+file, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	if w := hit(export.MetaFileName); w.Code != http.StatusOK {
		t.Fatalf("meta before delete: %d %s", w.Code, w.Body)
	}
	if _, err := dbtest.DeleteSnapshotID(ctx, store.root, newest); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{export.HexMetricsFileName, export.HexGridFileName, export.BoundaryFileName, export.ScenariosFileName, export.ForecastFileName} {
		w := hit(file)
		if w.Code != http.StatusGone {
			t.Errorf("%s after delete: %d, want 410", file, w.Code)
			continue
		}
		if !strings.Contains(w.Body.String(), "restart `pvmt serve`") {
			t.Errorf("%s 410 body = %q, want the restart advice", file, w.Body)
		}
	}
	// Only the meta.json built before the delete is cached: no 410 was kept.
	srv.cache.Range(func(key, _ any) bool {
		if key != cacheKey("meta", pinned.Slug, 0) {
			t.Errorf("cache holds %v after the delete; a 410 must not be cached", key)
		}
		return true
	})
}

// TestDataFile_SnapshotAtAnotherEdgeRenders: ?snapshot=N for a snapshot
// computed under other settings, at another hex_edge_m and before the
// boundary was re-ingested, renders on its own lattice and boundary: every
// stored hex id lands on a hex, and the boundary served is the one it froze.
func TestDataFile_SnapshotAtAnotherEdgeRenders(t *testing.T) {
	ctx := context.Background()
	oldCfg := &config.Config{Cities: []config.CityConfig{{Name: "Test City", HexEdgeM: 150}}}
	cfg := &config.Config{Cities: []config.CityConfig{{Name: "Test City"}}} // 100 m now
	entry, store, _ := sqliteEntry(t, cfg, cfg.Cities[0])

	// commitGrid commits a roads snapshot under c's settings with coverage on
	// every hex of the lattice compute would tile at that edge.
	commitGrid := func(c *config.Config) string {
		t.Helper()
		city := c.Cities[0]
		edge := c.ResolvedHexEdge(&city)
		gj, err := store.GetBoundary(ctx)
		if err != nil {
			t.Fatal(err)
		}
		bbox, err := geo.BBoxFromGeoJSON(gj)
		if err != nil {
			t.Fatal(err)
		}
		var cov []results.HexCoverage
		for _, h := range geo.CityLattice(geo.NewUTMProjector(geo.CenterFromBBox(bbox)), bbox, edge) {
			cov = append(cov, results.HexCoverage{HexID: h.ID, Area: 10, PctCovered: 1})
		}
		hash := c.CityHash(&city)
		run, err := store.CreateRun(ctx, "roads", hash, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveCityMetrics(ctx, run.ID, results.TotalRows(resource.TypeRoads, results.Totals{Area: 1, Features: 1})); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveHexMetrics(ctx, run.ID, results.HexCoverageRows(resource.TypeRoads, cov)); err != nil {
			t.Fatal(err)
		}
		snap, err := dbtest.Publish(ctx, store.root, store.id, hash, edge, map[string]int64{"roads": run.ID})
		if err != nil {
			t.Fatal(err)
		}
		return snap.Name
	}
	old := commitGrid(oldCfg)
	const shifted = `{"type":"Polygon","coordinates":[[[-121.80,37.64],[-121.68,37.64],[-121.68,37.72],[-121.80,37.72],[-121.80,37.64]]]}`
	if err := store.SaveBoundary(ctx, shifted, "test"); err != nil {
		t.Fatal(err)
	}
	commitGrid(cfg)
	pinned, err := entry.PinNewestSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}

	ios, _, _, _ := iostreams.Test()
	srv := New([]export.CityEntry{pinned}, "127.0.0.1", 0, ios, testAssets(t))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /data/{file}", srv.handleDataFile(pinned))
	get := func(url string, v any) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", url, w.Code, w.Body)
		}
		if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
			t.Fatal(err)
		}
	}
	type grid struct {
		Features []struct {
			Properties map[string]any `json:"properties"`
		} `json:"features"`
	}
	counts := func(url string) (hexes, covered int) {
		t.Helper()
		var g grid
		get(url, &g)
		for _, f := range g.Features {
			if _, ok := f.Properties["all"]; ok {
				covered++
			}
		}
		return len(g.Features), covered
	}

	oldHexes, oldCovered := counts(fmt.Sprintf("/data/%s?snapshot=%s", export.HexGridFileName, old))
	newHexes, newCovered := counts("/data/" + export.HexGridFileName)
	if oldHexes == 0 || oldCovered != oldHexes {
		t.Errorf("snapshot %s: %d of %d hexes covered, want every one (its ids on its own 150 m lattice)", old, oldCovered, oldHexes)
	}
	if newCovered != newHexes {
		t.Errorf("latest: %d of %d hexes covered, want every one", newCovered, newHexes)
	}
	// 150 m hexes are (100/150)² as many as 100 m ones over a larger area.
	if oldHexes >= newHexes {
		t.Errorf("snapshot %s drew %d hexes, the 100 m latest %d: want the coarser 150 m grid", old, oldHexes, newHexes)
	}

	boundaryOf := func(url string) string {
		t.Helper()
		var fc struct {
			Features []struct {
				Geometry json.RawMessage `json:"geometry"`
			} `json:"features"`
		}
		get(url, &fc)
		if len(fc.Features) != 1 {
			t.Fatalf("%s: %d features", url, len(fc.Features))
		}
		return string(fc.Features[0].Geometry)
	}
	oldB := boundaryOf(fmt.Sprintf("/data/%s?snapshot=%s", export.BoundaryFileName, old))
	newB := boundaryOf("/data/" + export.BoundaryFileName)
	if !strings.Contains(oldB, "-121.84") || strings.Contains(newB, "-121.84") {
		t.Errorf("boundaries = %s / %s, want the frozen one for snapshot %s and the re-ingested one for latest", oldB, newB, old)
	}
}
