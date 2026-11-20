package export

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/db/dbtest"
	"github.com/jcrussell/solvent-streets/internal/metric"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
	"github.com/jcrussell/solvent-streets/internal/services"
)

// drawnHexIDs returns the first n ids, in id order, of the hexes
// hexgrid.geojson draws for entry (DrawnGrid), failing when there are fewer.
func drawnHexIDs(t *testing.T, entry CityEntry, n int) []string {
	t.Helper()
	ids := drawnOrder(t, entry)
	if len(ids) < n {
		t.Fatalf("drawn grid has %d hexes, need %d", len(ids), n)
	}
	return ids[:n]
}

// drawnOrder is every hex id hexgrid.geojson draws for entry (DrawnGrid), in
// id order.
func drawnOrder(t *testing.T, entry CityEntry) []string {
	t.Helper()
	d, err := NewDrawnGrid(t.Context(), entry, nil).drawn()
	if err != nil {
		t.Fatalf("drawn grid: %v", err)
	}
	if !slices.IsSorted(d.order) {
		t.Fatal("drawn grid order is not sorted by id")
	}
	return d.order
}

// TestHexMetrics_CoverExactlyTheDrawnHexes: a
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
	withSlivers := drawnOrder(t, probe)
	probe.City.MinHexArea = 100_000
	interior := drawnHexIDs(t, probe, 1)[0]
	kept := map[string]bool{}
	for _, id := range drawnOrder(t, probe) {
		kept[id] = true
	}
	sliver := ""
	for _, id := range withSlivers {
		if !kept[id] {
			sliver = id
			break
		}
	}
	if sliver == "" {
		t.Fatal("fixture has no sliver hex under the threshold")
	}
	const water = "hex:99999:99999" // computed on, but not in the drawn grid

	entry := hexEntry(t, map[resource.Type][]results.HexCoverage{
		rtRoads: {{HexID: interior, Area: 100, Share: 0.1}, {HexID: sliver, Area: 5, Share: 0.9}},
	}, nil)
	entry.City.MinHexArea = 100_000
	store := entry.Store.(*dbtest.MockStore)
	store.Facilities = map[string][]db.RunFacility{db.ServiceProducer("fire"): {
		{ID: "osm:node:1", Lon: -122.41, Lat: 37.71}, {ID: "osm:node:2", Lon: -122.4, Lat: 37.7}, {ID: "osm:node:3", Lon: -122.4, Lat: 37.7},
	}}
	store.HexMetrics = append(store.HexMetrics,
		db.HexMetric{HexID: interior, Metric: metric.FireResponseS, Value: 300, Ref: "osm:node:1"},
		db.HexMetric{HexID: sliver, Metric: metric.FireResponseS, Value: 9000, Ref: "osm:node:2"},
		db.HexMetric{HexID: water, Metric: metric.FireResponseS, Value: 8000, Ref: "osm:node:3"},
		db.HexMetric{HexID: sliver, Metric: metric.FireResponseS, Subject: services.SubjectUnreachable},
		db.HexMetric{HexID: water, Metric: metric.FireResponseS, Subject: services.SubjectUnreachable},
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
	values := hm.Metrics[metric.FireResponseS].Values
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
	s := meta.Metrics[metric.FireResponseS]
	if s.HexCount != 1 || s.Max != 300 || s.UnreachableCount != 0 || s.PointCount != 1 {
		t.Errorf("summary = %+v, want the one drawn hex: count 1, max 300, no unreachable, 1 station", s)
	}
	if s.WithinThresholdShare == nil || *s.WithinThresholdShare != 1 {
		t.Errorf("share = %v, want 1 (the only drawn hex is within)", s.WithinThresholdShare)
	}
}

// TestDrawnGrid_OneGridGivesEqualIDSets pins the equal-id-set invariant on a shared
// grid, as an export and the live server build the files: with a metric row
// on every hex of the lattice (slivers and an id outside the grid included),
// hex-metrics.json's ids are exactly hexgrid.geojson's, built on one
// DrawnGrid, and meta.json's summary counts the same hexes.
func TestDrawnGrid_OneGridGivesEqualIDSets(t *testing.T) {
	ctx := context.Background()
	probe := hexEntry(t, nil, nil)
	lattice := append(drawnOrder(t, probe), "hex:99999:99999")

	entry := hexEntry(t, map[resource.Type][]results.HexCoverage{
		rtRoads: {{HexID: lattice[0], Area: 100, Share: 0.1}},
	}, nil)
	entry.City.MinHexArea = 100_000 // drops every clipped edge hex
	store := entry.Store.(*dbtest.MockStore)
	for i, id := range lattice {
		store.HexMetrics = append(store.HexMetrics, db.HexMetric{HexID: id, Metric: metric.FireResponseS, Value: float64(100 + i)})
	}

	grid := NewDrawnGrid(ctx, entry, nil)
	fc, err := BuildHexGeoJSONOn(ctx, entry, grid)
	if err != nil || fc == nil {
		t.Fatalf("BuildHexGeoJSONOn = (%v, %v)", fc, err)
	}
	var drawn []string
	for _, f := range fc["features"].([]map[string]any) {
		drawn = append(drawn, f["properties"].(map[string]any)["id"].(string))
	}
	hm, err := BuildHexMetricsOn(ctx, entry, grid)
	if err != nil {
		t.Fatal(err)
	}
	covered := slices.Sorted(maps.Keys(hm.Metrics[metric.FireResponseS].Values))
	if !slices.Equal(covered, drawn) {
		t.Errorf("hex-metrics.json covers %d hexes, hexgrid.geojson draws %d; want the same ids", len(covered), len(drawn))
	}
	if len(drawn) == 0 || len(drawn) >= len(lattice)-1 {
		t.Fatalf("fixture: %d of %d lattice hexes drawn; want some but not all", len(drawn), len(lattice))
	}
	meta, err := BuildMetaOn(ctx, entry, 0, grid)
	if err != nil {
		t.Fatal(err)
	}
	if got := meta.Metrics[metric.FireResponseS].HexCount; got != len(drawn) {
		t.Errorf("meta.json summary counts %d hexes, want the %d drawn", got, len(drawn))
	}
}

// TestDrawnGrid_KeepRowsReadsEachMetricOnce: meta.json's summaries and
// hex-metrics.json built over one grid that keeps rows, as the static export
// builds them, read each stored metric once between them; a grid that does
// not keep them (the live server's) reads it per file.
func TestDrawnGrid_KeepRowsReadsEachMetricOnce(t *testing.T) {
	ctx := context.Background()
	probe := hexEntry(t, nil, nil)
	lattice := drawnOrder(t, probe)

	for _, keep := range []bool{true, false} {
		entry := hexEntry(t, map[resource.Type][]results.HexCoverage{
			rtRoads: {{HexID: lattice[0], Area: 100, Share: 0.1}},
		}, nil)
		store := entry.Store.(*dbtest.MockStore)
		rows := &dbtest.MockStore{HexMetrics: append(slices.Clone(store.HexMetrics),
			db.HexMetric{HexID: lattice[0], Metric: metric.FireResponseS, Value: 120})}
		reads := 0
		store.ListHexMetricsFunc = func(ctx context.Context, key, subject string) ([]db.HexMetric, error) {
			if key == metric.FireResponseS && subject == "" {
				reads++
			}
			return rows.ListHexMetrics(ctx, key, subject)
		}

		grid := NewDrawnGrid(ctx, entry, nil)
		if keep {
			grid.keepRows()
		}
		if _, err := BuildMetaOn(ctx, entry, 0, grid); err != nil {
			t.Fatal(err)
		}
		hm, err := BuildHexMetricsOn(ctx, entry, grid)
		if err != nil || hm == nil || len(hm.Metrics[metric.FireResponseS].Values) != 1 {
			t.Fatalf("keep=%v: BuildHexMetricsOn = (%+v, %v), want the one fire hex", keep, hm, err)
		}
		want := 2
		if keep {
			want = 1
		}
		if reads != want {
			t.Errorf("keep=%v: fire rows read %d times, want %d", keep, reads, want)
		}
	}
}
