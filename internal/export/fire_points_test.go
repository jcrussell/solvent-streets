package export

import (
	"context"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/population"
	"github.com/jcrussell/solvent-streets/internal/services"
)

// TestMetricSummary_StationsInsideAndOutside pins solvent-streets-ern4.28:
// the fire summary splits the stations serving the city by whether they lie
// inside its boundary, and flags a station list that is probably incomplete
// (none inside, or fewer inside than services.Spec.SparsePer100k per 100,000
// residents) with the caveat the stats panel shows.
func TestMetricSummary_StationsInsideAndOutside(t *testing.T) {
	h := metricsHexIDs(t)
	// metricsEntry's boundary is the square -122.5..-122.4, 37.5..37.6.
	facilities := []db.RunFacility{
		{ID: "osm:node:in", Name: "Inside", Source: "osm", Lon: -122.45, Lat: 37.55},
		{ID: "osm:node:out", Name: "Across the line", Source: "osm", Lon: -122.35, Lat: 37.55},
		// Fetched in the 5 km margin but nearest to no hex: not serving.
		{ID: "osm:node:idle", Name: "Idle", Source: "osm", Lon: -122.46, Lat: 37.56},
	}
	fire := func(hex, ref string, v float64) db.HexMetric {
		return db.HexMetric{HexID: hex, Metric: services.MetricFire, Value: v, Ref: ref}
	}
	people := func(n float64) db.HexMetric {
		return db.HexMetric{HexID: h[0], Metric: population.KeyPopulation, Value: n}
	}
	floor := services.Registry["fire"].SparsePer100k
	if floor <= 0 {
		t.Fatal("the fire service has no completeness floor")
	}
	for _, tc := range []struct {
		name            string
		rows            []db.HexMetric
		inside, outside int
		caveat          string
	}{
		{
			name:   "one inside, one outside, enough per resident",
			rows:   []db.HexMetric{fire(h[0], "osm:node:in", 300), fire(h[1], "osm:node:out", 400), people(100_000 / (2 * floor))},
			inside: 1, outside: 1,
		},
		{
			name:   "too few inside for the population",
			rows:   []db.HexMetric{fire(h[0], "osm:node:in", 300), fire(h[1], "osm:node:out", 400), people(100_000 / (floor / 2))},
			inside: 1, outside: 1,
			caveat: "Station list from OpenStreetMap may be incomplete: few stations inside the city for its population",
		},
		{
			name:    "only a neighbour's station serves the city",
			rows:    []db.HexMetric{fire(h[0], "osm:node:out", 300), fire(h[1], "osm:node:out", 400)},
			outside: 1,
			caveat:  "Station list from OpenStreetMap may be incomplete: no station inside the city serves it",
		},
		{
			name:   "no census: only the none-inside test applies",
			rows:   []db.HexMetric{fire(h[0], "osm:node:in", 300)},
			inside: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta, err := BuildMeta(context.Background(), metricsEntry(t, tc.rows, facilities, nil), 0)
			if err != nil {
				t.Fatal(err)
			}
			s := meta.Metrics[services.MetricFire]
			if s.InsidePointCount == nil || s.OutsidePointCount == nil {
				t.Fatalf("summary = %+v, want the inside/outside split", s)
			}
			if *s.InsidePointCount != tc.inside || *s.OutsidePointCount != tc.outside || s.PointCount != tc.inside+tc.outside {
				t.Errorf("points = %d (%d inside, %d outside), want %d inside, %d outside",
					s.PointCount, *s.InsidePointCount, *s.OutsidePointCount, tc.inside, tc.outside)
			}
			if s.PointLabel != "Stations serving the city" {
				t.Errorf("point_label = %q", s.PointLabel)
			}
			if s.PointsCaveat != tc.caveat || s.PointsIncomplete != (tc.caveat != "") {
				t.Errorf("caveat = %q (incomplete %v), want %q", s.PointsCaveat, s.PointsIncomplete, tc.caveat)
			}
		})
	}
}

// TestMetricSummary_NoBoundaryNoSplit: without a readable boundary the split
// is unknown, so it is omitted and nothing is flagged, rather than every
// station counting as outside.
func TestMetricSummary_NoBoundaryNoSplit(t *testing.T) {
	sum := MetricSummaryJSON{PointCount: 2}
	sums := map[string]MetricSummaryJSON{services.MetricFire: sum}
	flagSparsePoints(sums)
	if got := sums[services.MetricFire]; got.PointsIncomplete || got.PointsCaveat != "" {
		t.Errorf("summary without a split = %+v, want no flag", got)
	}
}
