package db

import (
	"context"
	"errors"
	"testing"
)

// TestWithSnapshot_GoneFailsEveryRead: a view pinned to a snapshot that was
// deleted (or never existed, or is another city's) fails every
// snapshot-scoped read with ErrSnapshotGone instead of reading as an empty,
// never-computed city.
func TestWithSnapshot_GoneFailsEveryRead(t *testing.T) {
	ctx := context.Background()
	root, store, _ := openInputsStore(t)
	if err := store.SaveBoundary(ctx, geomA, "nominatim"); err != nil {
		t.Fatal(err)
	}
	_, s1 := commitRun(t, store, "h", "roads", roadsRun(t, store, 100))
	commitRun(t, store, "h", "roads", roadsRun(t, store, 200))
	otherID, err := root.EnsureCity(ctx, "b", "B", "test")
	if err != nil {
		t.Fatal(err)
	}

	pinned := store.WithSnapshot(s1, "v1")
	if got := roadsArea(t, pinned); got != 100 {
		t.Fatalf("pinned roads before delete = %v, want 100", got)
	}
	if ok, err := deleteSnapshotID(store, s1); err != nil || !ok {
		t.Fatalf("DeleteSnapshot = (%v, %v)", ok, err)
	}

	views := map[string]Store{
		"deleted":      pinned,
		"never":        store.WithSnapshot(9999, ""),
		"another city": root.ForCity(otherID).WithSnapshot(s1+1, ""),
	}
	for name, v := range views {
		reads := map[string]func() error{
			"ListCityMetrics": func() error { _, err := v.ListCityMetrics(ctx, []string{"roads"}); return err },
			"ListHexMetrics":  func() error { _, err := v.ListHexMetrics(ctx, "paved_area", "roads"); return err },
			"HasHexMetric":    func() error { _, err := v.HasHexMetric(ctx, "paved_area", []string{"roads"}); return err },
			"GetBoundary":     func() error { _, err := v.GetBoundary(ctx); return err },
		}
		for read, fn := range reads {
			if err := fn(); !errors.Is(err, ErrSnapshotGone) {
				t.Errorf("%s view: %s error = %v, want ErrSnapshotGone", name, read, err)
			}
		}
	}

	// The error names the snapshot the view was pinned to, never its id.
	if _, err := pinned.GetBoundary(ctx); err == nil || err.Error() != `snapshot "v1" no longer exists (deleted or pruned)` {
		t.Errorf("deleted view error = %v, want it to name snapshot \"v1\"", err)
	}
	// A pin without a name falls back to the id.
	if _, err := views["never"].GetBoundary(ctx); err == nil || err.Error() != "snapshot 9999 no longer exists (deleted or pruned)" {
		t.Errorf("unnamed view error = %v, want it to give the id 9999", err)
	}

	// The unpinned view and the surviving snapshot are unaffected.
	if got := roadsArea(t, store); got != 200 {
		t.Errorf("unpinned roads = %v, want 200", got)
	}
}

// TestWithSnapshot_GetBoundaryIsFrozen: a pinned view reads the boundary its
// snapshot froze, whatever was ingested since; a snapshot that froze none
// (the city had no boundary at commit) falls back to the live one.
func TestWithSnapshot_GetBoundaryIsFrozen(t *testing.T) {
	ctx := context.Background()
	_, store, _ := openInputsStore(t)
	_, s0 := commitRun(t, store, "h", "roads", nil) // no boundary yet
	if err := store.SaveBoundary(ctx, geomA, "nominatim"); err != nil {
		t.Fatal(err)
	}
	_, s1 := commitRun(t, store, "h", "parking", nil)
	if err := store.SaveBoundary(ctx, geomB, "nominatim"); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name string
		view Store
		want string
	}{
		{"pinned to s1", store.WithSnapshot(s1, ""), geomA},
		{"pinned to s0 (froze none)", store.WithSnapshot(s0, ""), geomB},
		{"unpinned", store, geomB},
		{"latest-compute view", store.WithLatestCompute("h"), geomB},
		{"pinned runs", store.WithRuns([]int64{1}), geomB},
	} {
		got, err := c.view.GetBoundary(ctx)
		if err != nil || got != c.want {
			t.Errorf("%s: GetBoundary = (%s, %v), want %s", c.name, got, err, c.want)
		}
	}
}

// TestViewSnapshot: the snapshot a view reads, with its edge and frozen
// boundary's digest; none for a city without one; ErrSnapshotGone for a
// deleted pin.
func TestViewSnapshot(t *testing.T) {
	ctx := context.Background()
	_, store, _ := openInputsStore(t)
	if snap, err := store.WithLatestCompute("h").ViewSnapshot(ctx); err != nil || snap != nil {
		t.Fatalf("no snapshot: ViewSnapshot = (%+v, %v), want (nil, nil)", snap, err)
	}
	_, s0 := commitRun(t, store, "h", "roads", nil) // no boundary
	if err := store.SaveBoundary(ctx, geomA, "nominatim"); err != nil {
		t.Fatal(err)
	}
	_, s1 := commitRun(t, store, "h", "parking", nil)

	for _, c := range []struct {
		view      Store
		want      int64
		hasDigest bool
	}{
		{store.WithSnapshot(s0, ""), s0, false},
		{store.WithSnapshot(s1, ""), s1, true},
		{store, s1, true},
	} {
		snap, err := c.view.ViewSnapshot(ctx)
		if err != nil || snap == nil || snap.ID != c.want || snap.HexEdgeM != testEdge || (snap.BoundaryDigest != "") != c.hasDigest {
			t.Errorf("ViewSnapshot = (%+v, %v), want snapshot %d at %v m, boundary digest %v", snap, err, c.want, testEdge, c.hasDigest)
		}
	}
	// A latest compute is not a snapshot, whether resolved live or pinned.
	for name, view := range map[string]Store{"latest": store.WithLatestCompute("h"), "runs": store.WithRuns([]int64{1})} {
		if snap, err := view.ViewSnapshot(ctx); err != nil || snap != nil {
			t.Errorf("%s: ViewSnapshot = (%+v, %v), want (nil, nil)", name, snap, err)
		}
	}
	if _, err := deleteSnapshotID(store, s0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WithSnapshot(s0, "").ViewSnapshot(ctx); !errors.Is(err, ErrSnapshotGone) {
		t.Errorf("deleted pin: err = %v, want ErrSnapshotGone", err)
	}
}

// TestWithRuns: a view of pinned runs reads exactly them, whatever is
// committed later, and fails with ErrSnapshotGone once gc has swept any.
func TestWithRuns(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	roads := commitLatestRun(t, store, "roads", roadsRun(t, store, 100))
	pinned := store.WithRuns([]int64{roads})
	commitLatestRun(t, store, "roads", roadsRun(t, store, 200))
	if got := roadsArea(t, pinned); got != 100 {
		t.Errorf("pinned runs roads = %v, want 100 (a later commit must not mix in)", got)
	}
	if got := roadsArea(t, store.WithLatestCompute("h")); got != 200 {
		t.Errorf("live latest compute roads = %v, want 200", got)
	}
	if err := store.DiscardRun(ctx, roads); err != nil { // as gc would, once unreferenced
		t.Fatal(err)
	}
	if _, err := pinned.ListCityMetrics(ctx, []string{"roads"}); !errors.Is(err, ErrSnapshotGone) {
		t.Errorf("swept run: err = %v, want ErrSnapshotGone", err)
	}
}
