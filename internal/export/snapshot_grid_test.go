package export

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
)

// commitGrid commits a roads snapshot under cfg's settings for the city (its
// hash and hex edge) holding cov, and returns the snapshot id.
func (c *sqliteCity) commitGrid(cfg *config.Config, city config.CityConfig, cov []results.HexCoverage) int64 {
	c.t.Helper()
	ctx := context.Background()
	hash := cfg.CityHash(&city)
	run, err := c.store.CreateRun(ctx, "roads", hash, nil)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := c.store.SaveCityMetrics(ctx, run.ID, results.TotalRows(resource.TypeRoads, results.Totals{Area: 1, Features: 1})); err != nil {
		c.t.Fatal(err)
	}
	if err := c.store.SaveHexMetrics(ctx, run.ID, results.HexCoverageRows(resource.TypeRoads, cov)); err != nil {
		c.t.Fatal(err)
	}
	snap, err := dbtest.Publish(ctx, c.root, c.id, hash, cfg.ResolvedHexEdge(&city), map[string]int64{"roads": run.ID})
	if err != nil {
		c.t.Fatal(err)
	}
	return snap.ID
}

// expectedGrid is the clipped, sliver-filtered grid compute tiles boundary
// with at edge: what a snapshot computed there must render on.
func expectedGrid(t *testing.T, boundary string, edge, minArea float64) []string {
	t.Helper()
	bbox, err := geo.BBoxFromGeoJSON(boundary)
	if err != nil {
		t.Fatal(err)
	}
	proj := geo.NewUTMProjector(geo.CenterFromBBox(bbox))
	hexes, err := geo.ClipHexesToBoundaryGeoJSON(context.Background(), geo.CityLattice(proj, bbox, edge), boundary, proj, nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, h := range filterHexSlivers(hexes, minArea) {
		ids = append(ids, h.ID)
	}
	slices.Sort(ids)
	return ids
}

// coverageFor is one paved row per id.
func coverageFor(ids []string) []results.HexCoverage {
	cov := make([]results.HexCoverage, len(ids))
	for i, id := range ids {
		cov[i] = results.HexCoverage{HexID: id, Area: 10, Share: 0.01}
	}
	return cov
}

// gridIDs lists hexgrid.geojson's feature ids, sorted, and how many carry
// coverage.
func gridIDs(t *testing.T, fc map[string]any) (ids []string, covered int) {
	t.Helper()
	features, _ := fc["features"].([]map[string]any)
	for _, f := range features {
		props := f["properties"].(map[string]any)
		ids = append(ids, props["id"].(string))
		if _, ok := props["all"]; ok {
			covered++
		}
	}
	slices.Sort(ids)
	return ids, covered
}

// TestSnapshot_RendersOnItsOwnEdgeAndBoundary: a snapshot renders on the
// lattice it was computed on, its own hex edge clipped to the boundary it
// froze, after the config's hex_edge_m and the city's boundary have both
// changed; the Config tab shows that edge.
func TestSnapshot_RendersOnItsOwnEdgeAndBoundary(t *testing.T) {
	ctx := context.Background()
	oldCfg := &config.Config{Cities: []config.CityConfig{{Name: "Edge City", HexEdgeM: 150}}}
	cfg := &config.Config{Cities: []config.CityConfig{{Name: "Edge City"}}} // 100 m now
	c := newSQLiteCity(t, cfg, "Edge City", exportBoundaryA)
	oldIDs := expectedGrid(t, exportBoundaryA, 150, cfg.ResolvedMinHexArea(&cfg.Cities[0]))
	old := c.commitGrid(oldCfg, oldCfg.Cities[0], coverageFor(oldIDs))

	// Re-ingested boundary and a recompute under the current settings.
	if err := c.store.SaveBoundary(ctx, exportBoundaryB, "test"); err != nil {
		t.Fatal(err)
	}
	newIDs := expectedGrid(t, exportBoundaryB, 100, cfg.ResolvedMinHexArea(&cfg.Cities[0]))
	c.commitGrid(cfg, cfg.Cities[0], coverageFor(newIDs))

	pinned := c.entry().WithSnapshot(old)
	if edge, err := pinned.HexEdge(ctx); err != nil || edge != 150 {
		t.Fatalf("HexEdge = (%v, %v), want the snapshot's 150", edge, err)
	}
	bbox, lon, lat, err := pinned.BBoxAndCenter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := geo.BBoxFromGeoJSON(exportBoundaryA); bbox != want {
		t.Errorf("bbox = %v, want the frozen boundary's %v", bbox, want)
	}
	fc, err := BuildHexGeoJSON(ctx, pinned, geo.NewUTMProjector(lon, lat))
	if err != nil {
		t.Fatal(err)
	}
	ids, covered := gridIDs(t, fc)
	if !slices.Equal(ids, oldIDs) || covered != len(oldIDs) {
		t.Errorf("pinned grid: %d hexes (%d covered), want the %d of the 150 m grid over the old boundary, all covered",
			len(ids), covered, len(oldIDs))
	}

	latest, err := c.entry().PinNewestSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if edge, _ := latest.HexEdge(ctx); edge != 100 {
		t.Errorf("latest HexEdge = %v, want 100", edge)
	}
	_, lon, lat, _ = latest.BBoxAndCenter(ctx)
	fc, err = BuildHexGeoJSON(ctx, latest, geo.NewUTMProjector(lon, lat))
	if err != nil {
		t.Fatal(err)
	}
	if ids, covered := gridIDs(t, fc); !slices.Equal(ids, newIDs) || covered != len(newIDs) {
		t.Errorf("latest grid: %d hexes (%d covered), want %d", len(ids), covered, len(newIDs))
	}

	edges, err := HexEdges(ctx, []CityEntry{pinned})
	if err != nil {
		t.Fatal(err)
	}
	if toml := ResolvedTOMLWithEdges(cfg, edges); !strings.Contains(toml, "hex_edge_m = 150.0") {
		t.Errorf("Config tab does not show the snapshot's 150 m edge:\n%s", toml)
	}
}

// TestSliverThresholdWarning: min_hex_area is checked against the edge the
// snapshot renders at, which can be finer than the config's.
func TestSliverThresholdWarning(t *testing.T) {
	cfg := &config.Config{Grid: config.GridConfig{MinHexArea: 10000}}
	entry := CityEntry{Config: cfg, City: config.CityConfig{Name: "Fine City"}}
	// 100 m hexes are ~26k m², 50 m ones ~6.5k m².
	if w := sliverThresholdWarning(entry, 100); w != "" {
		t.Errorf("100 m edge: unexpected warning %q", w)
	}
	if w := sliverThresholdWarning(entry, 50); !strings.Contains(w, "min_hex_area") || !strings.Contains(w, "Fine City") {
		t.Errorf("50 m edge: warning = %q, want one naming the city and min_hex_area", w)
	}
}
