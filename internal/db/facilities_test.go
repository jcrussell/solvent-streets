package db

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// TestRunFacilities_ReadFromTheResolvedSnapshot: the facilities a service run
// recorded are read back through the view, like its metric rows: the pinned
// snapshot's run, only for the producer asked, and ErrSnapshotGone once the
// pinned snapshot is deleted.
func TestRunFacilities_ReadFromTheResolvedSnapshot(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	old := []RunFacility{{ID: "osm:node:9", Name: "Nine", Source: "osm", Lon: 1, Lat: 2}}
	moved := []RunFacility{
		{ID: "osm:node:9", Name: "Nine", Source: "osm", Lon: 3, Lat: 4},
		{ID: "config:fire:5,6", Name: "Manual", Source: "config", Lon: 5, Lat: 6},
	}
	save := func(fs []RunFacility) func(int64) {
		return func(run int64) {
			if err := store.SaveRunFacilities(ctx, run, fs); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, s1 := commitRun(t, store, "h", "service:fire", save(old))
	commitRun(t, store, "h", "service:fire", save(moved))
	commitRun(t, store, "h", "roads", nil)

	list := func(v Store, producer string) []RunFacility {
		t.Helper()
		fs, err := v.ListRunFacilities(ctx, producer)
		if err != nil {
			t.Fatal(err)
		}
		return fs
	}
	if got := list(store.WithSnapshot(s1), "service:fire"); !slices.Equal(got, old) {
		t.Errorf("pinned = %+v, want %+v", got, old)
	}
	want := []RunFacility{moved[1], moved[0]} // ordered by id
	if got := list(store, "service:fire"); !slices.Equal(got, want) {
		t.Errorf("newest = %+v, want %+v", got, want)
	}
	if got := list(store, "service:police"); got != nil {
		t.Errorf("another producer = %+v, want none", got)
	}

	if _, err := deleteSnapshotID(store, s1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WithSnapshot(s1).ListRunFacilities(ctx, "service:fire"); !errors.Is(err, ErrSnapshotGone) {
		t.Errorf("deleted pin: err = %v, want ErrSnapshotGone", err)
	}
}
