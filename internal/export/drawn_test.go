package export

import (
	"context"
	"slices"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
	"github.com/jcrussell/solvent-streets/internal/services"
)

// drawnHexIDs returns the first n ids, in id order, of the hexes
// hexgrid.geojson draws for entry (drawnGrid), failing when there are fewer.
func drawnHexIDs(t *testing.T, entry CityEntry, n int) []string {
	t.Helper()
	hexes, err := newDrawnGrid(t.Context(), entry, nil).hexes()
	if err != nil {
		t.Fatalf("drawn grid: %v", err)
	}
	ids := make([]string, 0, len(hexes))
	for _, h := range hexes {
		ids = append(ids, h.ID)
	}
	slices.Sort(ids)
	if len(ids) < n {
		t.Fatalf("drawn grid has %d hexes, need %d", len(ids), n)
	}
	return ids[:n]
}

// TestHexMetrics_CoverExactlyTheDrawnHexes pins solvent-streets-ern4.33: a
// stored metric row whose hex hexgrid.geojson does not draw — an edge sliver
// under min_hex_area, or an id outside the clipped grid such as interior
// water — is in neither hex-metrics.json nor meta.json's summary. The panel
// counts only what the map can paint.
func TestHexMetrics_CoverExactlyTheDrawnHexes(t *testing.T) {
	ctx := context.Background()
	// A full 200 m-edge hex is ~103.9k m²: a 100k threshold drops every
	// clipped edge hex, so a sliver is a hex of the default grid that is not
	// in this one.
	probe := hexEntry(t, nil, nil)
	withSlivers, err := newDrawnGrid(ctx, probe, nil).hexes()
	if err != nil {
		t.Fatal(err)
	}
	probe.City.MinHexArea = 100_000
	interior := drawnHexIDs(t, probe, 1)[0]
	kept := map[string]bool{}
	hexes, err := newDrawnGrid(ctx, probe, nil).hexes()
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hexes {
		kept[h.ID] = true
	}
	sliver := ""
	for _, h := range withSlivers {
		if !kept[h.ID] {
			sliver = h.ID
			break
		}
	}
	if sliver == "" {
		t.Fatal("fixture has no sliver hex under the threshold")
	}
	const water = "hex:99999:99999" // computed on, but not in the drawn grid

	entry := hexEntry(t, map[resource.Type][]results.HexCoverage{
		rtRoads: {{HexID: interior, Area: 100, PctCovered: 10}, {HexID: sliver, Area: 5, PctCovered: 90}},
	}, nil)
	entry.City.MinHexArea = 100_000
	store := entry.Store.(*dbtest.MockStore)
	store.HexMetrics = append(store.HexMetrics,
		db.HexMetric{HexID: interior, Metric: services.MetricFire, Value: 300, Ref: "osm:node:1"},
		db.HexMetric{HexID: sliver, Metric: services.MetricFire, Value: 9000, Ref: "osm:node:2"},
		db.HexMetric{HexID: water, Metric: services.MetricFire, Value: 8000, Ref: "osm:node:3"},
		db.HexMetric{HexID: sliver, Metric: services.MetricFire, Subject: services.SubjectUnreachable},
		db.HexMetric{HexID: water, Metric: services.MetricFire, Subject: services.SubjectUnreachable},
	)

	fc, err := BuildHexGeoJSON(ctx, entry, projectorFor(t, entry))
	if err != nil || fc == nil {
		t.Fatalf("BuildHexGeoJSON = (%v, %v)", fc, err)
	}
	features := map[string]bool{}
	for _, f := range fc["features"].([]map[string]any) {
		features[f["properties"].(map[string]any)["id"].(string)] = true
	}
	if !features[interior] || features[sliver] || features[water] {
		t.Fatalf("fixture: hexgrid draws interior %v, sliver %v, water %v; want only the interior hex",
			features[interior], features[sliver], features[water])
	}

	hm, err := BuildHexMetrics(ctx, entry)
	if err != nil {
		t.Fatal(err)
	}
	values := hm.Metrics[services.MetricFire].Values
	for id := range values {
		if !features[id] {
			t.Errorf("hex-metrics.json carries %s, which hexgrid.geojson does not draw", id)
		}
	}
	if len(values) != 1 || values[interior] != 300 {
		t.Errorf("values = %v, want only the interior hex", values)
	}

	meta, err := BuildMeta(ctx, entry, 0)
	if err != nil {
		t.Fatal(err)
	}
	s := meta.Metrics[services.MetricFire]
	if s.HexCount != 1 || s.Max != 300 || s.UnreachableCount != 0 || s.PointCount != 1 {
		t.Errorf("summary = %+v, want the one drawn hex: count 1, max 300, no unreachable, 1 station", s)
	}
	if s.WithinThresholdShare == nil || *s.WithinThresholdShare != 1 {
		t.Errorf("share = %v, want 1 (the only drawn hex is within)", s.WithinThresholdShare)
	}
}
