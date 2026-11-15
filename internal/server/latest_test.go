package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestDataFile_SnapshotLatest: ?snapshot=latest serves the latest compute
// the server read at startup, marked latest_compute in meta.json and dated by
// its newest run, beside the pinned snapshot (snapshot_id, dated by its
// created_at), and a city with no snapshot serves its latest compute by
// default.
func TestDataFile_SnapshotLatest(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{Cities: []config.CityConfig{{Name: "Test City"}}}
	city := cfg.Cities[0]
	entry, store, commit := sqliteEntry(t, cfg, city)
	snapID := commit(100)
	hash := cfg.CityHash(&city)
	run, err := store.CreateRun(ctx, "roads", hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCityMetrics(ctx, run.ID, results.TotalRows(resource.TypeRoads, results.Totals{Area: 300, Features: 1})); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitLatest(ctx, hash, config.ComputeEpoch, map[string]int64{"roads": run.ID}); err != nil {
		t.Fatal(err)
	}
	latest, err := store.LatestCompute(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := entry.PinNewestSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pinned.Latest = latest

	meta := func(e export.CityEntry, query string) export.MetaJSON {
		t.Helper()
		ios, _, _, _ := iostreams.Test()
		srv := New([]export.CityEntry{e}, "127.0.0.1", 0, ios, testAssets(t))
		mux := http.NewServeMux()
		mux.HandleFunc("GET /data/{file}", srv.handleDataFile(e))
		req, _ := http.NewRequestWithContext(ctx, "GET", "/data/"+export.MetaFileName+query, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("meta%s: %d %s", query, w.Code, w.Body)
		}
		var m export.MetaJSON
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	roads := func(m export.MetaJSON) float64 {
		for _, s := range m.Stats {
			if s.ResourceType == "roads" {
				return s.TotalArea
			}
		}
		return -1
	}

	def := meta(pinned, "")
	if def.SnapshotID != snapID || def.LatestCompute || roads(def) != 100 {
		t.Errorf("default view = snapshot %d latest %v roads %v, want snapshot %d's 100", def.SnapshotID, def.LatestCompute, roads(def), snapID)
	}
	lc := meta(pinned, "?snapshot=latest")
	if lc.SnapshotID != 0 || !lc.LatestCompute || roads(lc) != 300 {
		t.Errorf("?snapshot=latest = snapshot %d latest %v roads %v, want the latest compute's 300", lc.SnapshotID, lc.LatestCompute, roads(lc))
	}
	if want := latest[0].ComputedAt.Format("2006-01-02"); lc.SnapshotDate != want {
		t.Errorf("latest compute snapshot_date = %q, want its run's %q", lc.SnapshotDate, want)
	}

	// A city with no snapshot: the latest compute is its default view.
	unsnapped := export.LatestComputeEntry(entry, latest)
	if m := meta(unsnapped, ""); !m.LatestCompute || roads(m) != 300 {
		t.Errorf("no-snapshot city default = latest %v roads %v, want its latest compute", m.LatestCompute, roads(m))
	}
}
