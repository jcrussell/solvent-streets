package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

const goneBoundary = `{"type":"Polygon","coordinates":[[[-121.84,37.64],[-121.68,37.64],[-121.68,37.72],[-121.84,37.72],[-121.84,37.64]]]}`

// sqliteEntry returns a city in a real in-memory database with goneBoundary
// stored, scoped to its current hash as BuildCityEntries makes it, and a
// commit func that adds a roads snapshot and returns its id.
func sqliteEntry(t *testing.T, cfg *config.Config, city config.CityConfig) (export.CityEntry, db.Store, func(area float64) int64) {
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
	store := root.ForCity(id)
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
		snap, err := store.CommitSnapshot(ctx, hash, cfg.ResolvedHexEdge(&city), map[string]int64{"roads": run.ID})
		if err != nil {
			t.Fatal(err)
		}
		return snap.ID
	}
	entry := export.CityEntry{Config: cfg, City: city, Store: store.WithConfigHash(cfg.CityHash(&city)), Slug: city.Slug()}
	return entry, store, commit
}

// TestDataFile_PinnedSnapshotDeletedIs410: when the snapshot serve pinned at
// startup is deleted, every uncached data file answers 410 Gone saying to
// restart, not 200 with an empty city, and the failure is not cached
// (solvent-streets-jxdj).
func TestDataFile_PinnedSnapshotDeletedIs410(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{Cities: []config.CityConfig{{Name: "Test City"}}}
	entry, store, commit := sqliteEntry(t, cfg, cfg.Cities[0])
	commit(100)
	newest := commit(200)
	pinned, err := entry.PinCurrentSnapshot(ctx)
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
	if _, err := store.DeleteSnapshot(ctx, newest); err != nil {
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
		if _, cached := srv.cache.Load(cacheKey(cacheKind(file), pinned.Slug, 0)); cached {
			t.Errorf("%s: the 410 was left in the cache", file)
		}
	}
}

// cacheKind maps a data file to its serveJSONCached key kind.
func cacheKind(file string) string {
	return map[string]string{
		export.HexMetricsFileName: "hexmetrics",
		export.HexGridFileName:    "hexgrid",
		export.BoundaryFileName:   "boundary",
		export.ScenariosFileName:  "scenarios",
		export.ForecastFileName:   "forecast",
	}[file]
}
