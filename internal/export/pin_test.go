package export

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
)

// sqliteCity is one city in a real in-memory database, for tests that need
// the store's snapshot semantics rather than a mock's.
type sqliteCity struct {
	t     *testing.T
	cfg   *config.Config
	city  config.CityConfig
	store db.Store
}

// newSQLiteCity opens an in-memory database holding one city with boundary
// stored.
func newSQLiteCity(t *testing.T, cfg *config.Config, name, boundary string) *sqliteCity {
	t.Helper()
	ctx := context.Background()
	root, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	city := config.CityConfig{Name: name}
	id, err := root.EnsureCity(ctx, city.Slug(), name, "test")
	if err != nil {
		t.Fatal(err)
	}
	store := root.ForCity(id)
	if boundary != "" {
		if err := store.SaveBoundary(ctx, boundary, "test"); err != nil {
			t.Fatal(err)
		}
	}
	return &sqliteCity{t: t, cfg: cfg, city: city, store: store}
}

// commitRoads commits a snapshot whose roads part holds area, under the
// city's current hash and edge, and returns the snapshot id.
func (c *sqliteCity) commitRoads(area float64) int64 {
	c.t.Helper()
	ctx := context.Background()
	hash := c.cfg.CityHash(&c.city)
	run, err := c.store.CreateRun(ctx, "roads", hash, nil)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := c.store.SaveCityMetrics(ctx, run.ID, results.TotalRows(resource.TypeRoads, results.Totals{Area: area, Features: 1})); err != nil {
		c.t.Fatal(err)
	}
	snap, err := dbtest.Publish(ctx, c.store, hash, c.cfg.ResolvedHexEdge(&c.city), map[string]int64{"roads": run.ID})
	if err != nil {
		c.t.Fatal(err)
	}
	return snap.ID
}

// entry is the city as BuildCityEntries makes it: scoped to its current hash.
func (c *sqliteCity) entry() CityEntry {
	return CityEntry{Config: c.cfg, City: c.city, Store: c.store.WithLatestCompute(c.cfg.CityHash(&c.city)), Slug: c.city.Slug()}
}

// TestExport_PinnedSnapshotDeletedFails: a snapshot deleted while an export
// holds it pinned fails the export with db.ErrSnapshotGone, never publishing
// the city as empty, and nothing is written for the city (solvent-streets-jxdj).
func TestExport_PinnedSnapshotDeletedFails(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{}
	c := newSQLiteCity(t, cfg, "Gone City", exportBoundaryA)
	c.commitRoads(100)
	newest := c.commitRoads(200)

	entries := []CityEntry{c.entry()}
	if err := PinNewest(ctx, entries, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	pinned := entries[0]
	if _, err := c.store.DeleteSnapshot(ctx, newest); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	e := New([]CityEntry{pinned}, cfg, dir, "metric", testAssets(t))
	if _, _, err := e.exportCityData(ctx, pinned, dir); !errors.Is(err, db.ErrSnapshotGone) {
		t.Errorf("exportCityData error = %v, want ErrSnapshotGone", err)
	}
	if _, err := os.Stat(filepath.Join(dir, MetaFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("meta.json written for a deleted snapshot (stat err %v)", err)
	}
	if _, err := BuildMeta(ctx, pinned, 0); !errors.Is(err, db.ErrSnapshotGone) {
		t.Errorf("BuildMeta error = %v, want ErrSnapshotGone", err)
	}

	// The multi-city aggregates fail too, rather than leaving the city out.
	other := newSQLiteCity(t, cfg, "Other City", exportBoundaryB)
	other.commitRoads(50)
	if _, err := BuildMultiCityMeta(ctx, []CityEntry{other.entry(), pinned}, "Region"); !errors.Is(err, db.ErrSnapshotGone) {
		t.Errorf("BuildMultiCityMeta error = %v, want ErrSnapshotGone", err)
	}
}
