package db

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// TestListMetrics_OrderedWhateverTheWriteOrder pins the read order seeds,
// compare.json and multi-city aggregation rely on, against real SQLite:
// rows come back sorted by (subject, class, metric) and hex rows by hex id,
// whatever order they were written in, so two exports of one database are
// identical. dbtest.MockStore sorts by itself, so only
// a real-store test catches a dropped ORDER BY.
func TestListMetrics_OrderedWhateverTheWriteOrder(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	classes := []string{"tertiary", "primary", "service", "residential", "motorway"}
	hexes := []string{"hex:9:1", "hex:1:2", "hex:5:5", "hex:3:0"}
	commitRun(t, store, "h", "roads", func(run int64) {
		var city []CityMetric
		for _, c := range classes {
			// metric order reversed too, so (class, metric) both need sorting.
			city = append(city,
				CityMetric{Metric: "raw_area_m2", Subject: "roads", Class: c, Value: 1},
				CityMetric{Metric: "paved_area_m2", Subject: "roads", Class: c, Value: 2})
		}
		if err := store.SaveCityMetrics(ctx, run, city); err != nil {
			t.Fatal(err)
		}
		var hex []HexMetric
		for _, h := range hexes {
			hex = append(hex, HexMetric{HexID: h, Metric: "paved_area_m2", Subject: "roads", Value: 1})
		}
		if err := store.SaveHexMetrics(ctx, run, hex); err != nil {
			t.Fatal(err)
		}
	})

	got, err := store.ListCityMetrics(ctx, []string{"roads"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2*len(classes) {
		t.Fatalf("got %d city rows, want %d", len(got), 2*len(classes))
	}
	if !slices.IsSortedFunc(got, func(a, b CityMetric) int {
		if c := strings.Compare(a.Subject, b.Subject); c != 0 {
			return c
		}
		if c := strings.Compare(a.Class, b.Class); c != 0 {
			return c
		}
		return strings.Compare(a.Metric, b.Metric)
	}) {
		t.Errorf("ListCityMetrics is not ordered by (subject, class, metric): %+v", got)
	}

	hexRows, err := store.ListHexMetrics(ctx, "paved_area_m2", "roads")
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(hexRows))
	for i, h := range hexRows {
		ids[i] = h.HexID
	}
	if want := slices.Sorted(slices.Values(hexes)); !slices.Equal(ids, want) {
		t.Errorf("ListHexMetrics order = %v, want %v", ids, want)
	}
}
