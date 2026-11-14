package db

import (
	"context"
	"errors"
	"maps"
	"testing"
)

// TestLatestCompute: the latest compute lists each working part with its run,
// computed_at and recorded inputs, per config hash.
func TestLatestCompute(t *testing.T) {
	ctx := context.Background()
	_, store, _ := openInputsStore(t)
	if parts, err := store.LatestCompute(ctx, "h"); err != nil || parts != nil {
		t.Fatalf("empty LatestCompute = (%v, %v), want (nil, nil)", parts, err)
	}
	in := map[string]string{"boundary": "b1", "roads": "r1"}
	run, err := store.CreateRun(ctx, "roads", "h", in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitSnapshot(ctx, "h", testEdge, map[string]int64{"roads": run.ID}); err != nil {
		t.Fatal(err)
	}
	census, _ := commitRun(t, store, "h", "census", nil)

	parts, err := store.LatestCompute(ctx, "h")
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[0].Part != "census" || parts[1].Part != "roads" {
		t.Fatalf("parts = %+v, want census then roads", parts)
	}
	if parts[0].RunID != census || len(parts[0].Inputs) != 0 {
		t.Errorf("census part = %+v, want run %d with no inputs", parts[0], census)
	}
	if p := parts[1]; p.RunID != run.ID || !p.ComputedAt.Equal(run.ComputedAt) || !maps.Equal(p.Inputs, in) {
		t.Errorf("roads part = %+v, want run %d at %v reading %v", p, run.ID, run.ComputedAt, in)
	}
	if other, err := store.LatestCompute(ctx, "other"); err != nil || other != nil {
		t.Errorf("another hash's latest compute = (%v, %v), want none", other, err)
	}
}

// TestCreateSnapshot: create freezes the latest compute, is a no-op when the
// newest snapshot already freezes the same thing, and never creates an empty
// snapshot.
func TestCreateSnapshot(t *testing.T) {
	ctx := context.Background()
	_, store, _ := openInputsStore(t)

	if _, _, err := store.CreateSnapshot(ctx, "h", testEdge, nil); !errors.Is(err, ErrNothingComputed) {
		t.Fatalf("empty latest compute: err = %v, want ErrNothingComputed", err)
	}
	if _, _, err := store.CreateSnapshot(ctx, "", testEdge, nil); err == nil {
		t.Error("an empty config hash was accepted")
	}
	if _, _, err := store.CreateSnapshot(ctx, "h", 0, nil); err == nil {
		t.Error("a zero hex edge was accepted")
	}

	if err := store.SaveBoundary(ctx, geomA, "nominatim"); err != nil {
		t.Fatal(err)
	}
	_, s1 := commitRun(t, store, "h", "roads", nil)

	// The compute's own snapshot already freezes this: unchanged.
	snap, created, err := store.CreateSnapshot(ctx, "h", testEdge, nil)
	if err != nil || created || snap.ID != s1 {
		t.Fatalf("CreateSnapshot = (%+v, %v, %v), want snapshot %d unchanged", snap, created, err, s1)
	}
}

// TestCreateSnapshot_Check: the check sees the latest compute and the current
// inputs inside the transaction; a refusal writes nothing, and a passing
// check after a boundary change freezes the new boundary.
func TestCreateSnapshot_Check(t *testing.T) {
	ctx := context.Background()
	root, store, id := openInputsStore(t)
	snapshots := func() int {
		return countRows(t, root, `SELECT COUNT(*) FROM snapshots WHERE city_id = ?`, id)
	}
	if err := store.SaveBoundary(ctx, geomA, "nominatim"); err != nil {
		t.Fatal(err)
	}
	roads, s1 := commitRun(t, store, "h", "roads", nil)

	// The check sees the latest compute and the current inputs, and its
	// refusal is returned as is, with nothing written.
	refuse := errors.New("refused")
	var seen []LatestPart
	var seenInputs []InputState
	before := snapshots()
	if err := store.SaveBoundary(ctx, geomB, "nominatim"); err != nil {
		t.Fatal(err)
	}
	_, _, err := store.CreateSnapshot(ctx, "h", testEdge, func(parts []LatestPart, inputs []InputState) error {
		seen, seenInputs = parts, inputs
		return refuse
	})
	if !errors.Is(err, refuse) {
		t.Fatalf("refusing check: err = %v, want its error", err)
	}
	if len(seen) != 1 || seen[0].Part != "roads" || seen[0].RunID != roads {
		t.Errorf("check saw parts %+v, want roads run %d", seen, roads)
	}
	if len(seenInputs) != 1 || seenInputs[0].Input != InputBoundary || seenInputs[0].Digest != boundaryDigest(geomB, "nominatim") {
		t.Errorf("check saw inputs %+v, want the current boundary", seenInputs)
	}
	if n := snapshots(); n != before {
		t.Errorf("a refused create wrote %d snapshot(s)", n-before)
	}

	// A new boundary makes a new snapshot of the same manifest, frozen to it.
	snap, created, err := store.CreateSnapshot(ctx, "h", testEdge, func([]LatestPart, []InputState) error { return nil })
	if err != nil || !created || snap.ID == s1 {
		t.Fatalf("after a boundary change CreateSnapshot = (%+v, %v, %v), want a new snapshot", snap, created, err)
	}
	if snap.BoundaryDigest != boundaryDigest(geomB, "nominatim") || len(snap.Parts) != 1 || snap.Parts[0].RunID != roads {
		t.Errorf("new snapshot = %+v, want roads run %d on boundary B", snap, roads)
	}
}

// TestCreateSnapshot_Unchanged: a new edge makes a new snapshot, repeating
// it is unchanged, and the newest snapshot being another hash's makes the
// same latest compute new again.
func TestCreateSnapshot_Unchanged(t *testing.T) {
	ctx := context.Background()
	_, store, _ := openInputsStore(t)
	_, s1 := commitRun(t, store, "h", "roads", nil)

	// A new edge makes a new snapshot; repeating it is unchanged.
	snap, created, err := store.CreateSnapshot(ctx, "h", 2*testEdge, nil)
	if err != nil || !created || snap.HexEdgeM != 2*testEdge {
		t.Fatalf("new edge: CreateSnapshot = (%+v, %v, %v), want a new snapshot at %v m", snap, created, err, 2*testEdge)
	}
	again, created, err := store.CreateSnapshot(ctx, "h", 2*testEdge, nil)
	if err != nil || created || again.ID != snap.ID {
		t.Errorf("repeat: CreateSnapshot = (%+v, %v, %v), want %d unchanged", again, created, err, snap.ID)
	}
	if again.ID == s1 {
		t.Error("the repeat matched an older snapshot, not the newest")
	}

	// The newest snapshot being another hash's makes this one new again.
	commitRun(t, store, "other", "roads", nil)
	if _, created, err := store.CreateSnapshot(ctx, "h", 2*testEdge, nil); err != nil || !created {
		t.Errorf("after another hash's snapshot: created = %v, err = %v; want a new snapshot", created, err)
	}
}

// TestDraft_PinInput: a pinned digest replaces the producer's own reading of
// that input, for producers that read it, and leaves other producers alone.
func TestDraft_PinInput(t *testing.T) {
	ctx := context.Background()
	root, store, _ := openInputsStore(t)
	d := NewDraft(store, "h", testEdge)
	d.PinInput(InputBoundary, "old")
	read := map[string]string{InputBoundary: "new", "roads": "r1"}
	roads, err := d.NewRun(ctx, "roads", read)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := runInputs(t, root, roads), map[string]string{InputBoundary: "old", "roads": "r1"}; !maps.Equal(got, want) {
		t.Errorf("pinned run inputs = %v, want %v", got, want)
	}
	if read[InputBoundary] != "new" {
		t.Error("PinInput changed the caller's map")
	}
	other, err := d.NewRun(ctx, "census", map[string]string{"layer-census-blocks": "c1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := runInputs(t, root, other); len(got) != 1 || got["layer-census-blocks"] != "c1" {
		t.Errorf("a producer that does not read the pinned input recorded %v", got)
	}
}
