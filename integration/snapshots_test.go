package integration

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmd/all"
	"github.com/jcrussell/solvent-streets/pkg/cmd/compute"
	servicescmd "github.com/jcrussell/solvent-streets/pkg/cmd/services"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestSnapshots_RecomputeSwapsOnlyItsPart drives real commands against a real
// store through the snapshot lifecycle of epic solvent-streets-25t6:
//
//  1. `all compute` commits exactly ONE snapshot holding every step's part,
//     fire coverage included, and a census part only when there is census.
//  2. A standalone `roads compute` commits a new snapshot that swaps in a new
//     roads run AND a refreshed roads forecast, and carries every other part
//     (fire coverage, parking, the combined pass) forward untouched.
//  3. A services compute that finds no stations drops its part, so the old
//     coverage stops being published while everything else stays.
func TestSnapshots_RecomputeSwapsOnlyItsPart(t *testing.T) {
	ctx := context.Background()
	store := newAllFixtureStore(t, ctx)
	fire := resource.FacilityType("fire")
	station := feat("osm:node:1", fire, map[string]string{"amenity": "fire_station"},
		`{"type":"Point","coordinates":[-121.765,37.684]}`)
	station.SourceAPI = "overpass"
	if err := store.UpsertFeatures(ctx, fire, []db.Feature{station}, nil); err != nil {
		t.Fatal(err)
	}
	ios, _, _, stderr := iostreams.Test()
	f := allComputeFactory(ios, store)

	// 1. One `all compute`, one snapshot.
	allCmd := all.NewCmdAll(f)
	allCmd.SilenceErrors, allCmd.SilenceUsage = true, true
	allCmd.SetArgs([]string{"compute"})
	if err := allCmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("all compute: %v\nstderr:\n%s", err, stderr.String())
	}
	snaps := listSnapshots(t, store)
	if len(snaps) != 1 {
		t.Fatalf("all compute committed %d snapshots, want exactly 1: %+v", len(snaps), snaps)
	}
	first := partsOf(snaps[0])
	wantParts := []string{
		"combined", "forecast:parking", "forecast:roads", "forecast:sidewalks",
		"parking", "roads", "service:fire", "sidewalks",
	}
	if got := slices.Sorted(maps.Keys(first)); !slices.Equal(got, wantParts) {
		t.Errorf("all compute parts = %v, want %v (no census part: the fixture has no blocks)", got, wantParts)
	}
	fireRows := hexMetricCount(t, store, "fire_response_s")
	if fireRows == 0 {
		t.Fatal("fire coverage reached no hexes; the fixture station should reach the fixture roads")
	}

	// 2. A standalone roads recompute swaps roads and its forecast only.
	rcmd := compute.NewCmdCompute(f, &resource.Pavement{}, nil)
	rcmd.SilenceErrors, rcmd.SilenceUsage = true, true
	rcmd.SetArgs(nil)
	if err := rcmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("roads compute: %v\nstderr:\n%s", err, stderr.String())
	}
	snaps = listSnapshots(t, store)
	if len(snaps) != 2 {
		t.Fatalf("snapshots after roads compute = %d, want 2", len(snaps))
	}
	second := partsOf(snaps[0])
	for part, run := range first {
		swapped := part == "roads" || part == "forecast:roads"
		if swapped && second[part] == run {
			t.Errorf("%s still points at run %d after a roads recompute; want a new run", part, run)
		}
		if !swapped && second[part] != run {
			t.Errorf("%s changed from run %d to %d on a ROADS recompute; only roads and its forecast may change", part, run, second[part])
		}
	}
	if len(second) != len(first) {
		t.Errorf("parts after roads compute = %v, want the same set as %v", second, first)
	}
	if got := hexMetricCount(t, store, "fire_response_s"); got != fireRows {
		t.Errorf("fire coverage after a roads recompute = %d hexes, want the same %d (it used to vanish)", got, fireRows)
	}

	// 3. No stations left: the fire part is dropped, nothing else moves.
	if err := store.UpsertFeatures(ctx, fire, nil, nil); err != nil {
		t.Fatal(err)
	}
	err := servicescmd.RunComputeForCity(ctx, f, servicescmd.DefaultService, nil, nil)
	if !errors.Is(err, cmdutil.ErrNoResults) {
		t.Fatalf("services compute with no stations = %v, want ErrNoResults", err)
	}
	snaps = listSnapshots(t, store)
	third := partsOf(snaps[0])
	if _, ok := third["service:fire"]; ok {
		t.Errorf("parts after losing every station = %v, want service:fire dropped", third)
	}
	if third["roads"] != second["roads"] || third["parking"] != second["parking"] {
		t.Errorf("dropping fire moved other parts: %v -> %v", second, third)
	}
	if got := hexMetricCount(t, store, "fire_response_s"); got != 0 {
		t.Errorf("fire coverage still published (%d hexes) after every station was removed", got)
	}
}

func listSnapshots(t *testing.T, store db.Store) []db.Snapshot {
	t.Helper()
	snaps, err := store.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return snaps
}

func partsOf(s db.Snapshot) map[string]int64 {
	out := map[string]int64{}
	for _, p := range s.Parts {
		out[p.Part] = p.RunID
	}
	return out
}

func hexMetricCount(t *testing.T, store db.Store, metric string) int {
	t.Helper()
	rows, err := store.ListHexMetrics(context.Background(), metric)
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}
