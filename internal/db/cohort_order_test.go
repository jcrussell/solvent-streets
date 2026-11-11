package db

import (
	"context"
	"slices"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/resource"
)

// TestListCohortStats_OrderedByClassification pins the read order that seeds
// and multi-city aggregation depend on: rows come back sorted by
// classification whatever order they were written in, so two exports of one
// database are identical (solvent-streets-1qv7).
func TestListCohortStats_OrderedByClassification(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	written := []string{"tertiary", "primary", "service", "residential", "motorway"}
	commitRun(t, store, "h", "roads", func(run int64) {
		var stats []CohortStat
		for _, c := range written {
			stats = append(stats, CohortStat{ResourceType: rtRoads, Classification: c, Area: 1, RunID: run})
		}
		if err := store.SaveCohortStats(ctx, stats); err != nil {
			t.Fatal(err)
		}
	})
	want := slices.Sorted(slices.Values(written))

	got, err := store.ListCohortStats(ctx, rtRoads)
	if err != nil {
		t.Fatal(err)
	}
	if c := classesOf(got); !slices.Equal(c, want) {
		t.Errorf("ListCohortStats order = %v, want %v", c, want)
	}

	byType, err := store.ListCohortStatsForTypes(ctx, []resource.Type{rtRoads})
	if err != nil {
		t.Fatal(err)
	}
	if c := classesOf(byType[rtRoads]); !slices.Equal(c, want) {
		t.Errorf("ListCohortStatsForTypes order = %v, want %v", c, want)
	}
}

func classesOf(stats []CohortStat) []string {
	out := make([]string, len(stats))
	for i, s := range stats {
		out[i] = s.Classification
	}
	return out
}
