package db

import (
	"context"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/resource"
)

// openInputsStore opens a fresh database with one city and returns the root
// (for direct SQL) and the city's store.
func openInputsStore(t *testing.T) (*RootStore, Store, int64) {
	t.Helper()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	id, err := root.EnsureCity(context.Background(), "a", "A", "test")
	if err != nil {
		t.Fatal(err)
	}
	return root, root.ForCity(id), id
}

func feature(id, source, geometry string) Feature {
	return Feature{ID: id, Name: "n-" + id, Tags: map[string]string{"highway": "residential"},
		GeometryJSON: geometry, SourceAPI: source}
}

// inputState returns input's city_inputs row, failing when it is missing.
func inputState(t *testing.T, store Store, input string) InputState {
	t.Helper()
	states, err := store.InputStates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range states {
		if s.Input == input {
			return s
		}
	}
	t.Fatalf("no city_inputs row for %q in %+v", input, states)
	return InputState{}
}

// backdateInputs moves every ingested_at an hour into the past, so a test can
// tell whether a later write touched it (timestamps have second
// granularity).
func backdateInputs(t *testing.T, root *RootStore) time.Time {
	t.Helper()
	past := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if _, err := root.db.ExecContext(context.Background(), `UPDATE city_inputs SET ingested_at = ?`,
		past.Unix()); err != nil {
		t.Fatal(err)
	}
	return past
}

const (
	geomA = `{"type":"LineString","coordinates":[[0,0],[1,1]]}`
	geomB = `{"type":"LineString","coordinates":[[0,0],[2,2]]}`
)

// TestUpsertFeatures_Digest: an identical re-ingest keeps both the digest and
// ingested_at; a changed row (geometry, name, tags or source) moves both.
// Row order and fetched_at do not matter.
func TestUpsertFeatures_Digest(t *testing.T) {
	ctx := context.Background()
	root, store, _ := openInputsStore(t)
	rt := resource.TypeRoads
	base := []Feature{feature("w1", "overpass", geomA), feature("w2", "overpass", geomB)}
	if err := store.UpsertFeatures(ctx, rt, base, nil); err != nil {
		t.Fatal(err)
	}
	first := inputState(t, store, "roads")
	if first.Digest == "" {
		t.Fatal("digest of stored roads is empty")
	}
	past := backdateInputs(t, root)

	// Same rows, other order, later fetch time: unchanged.
	again := []Feature{base[1], base[0]}
	again[0].FetchedAt = time.Now().Add(time.Minute)
	if err := store.UpsertFeatures(ctx, rt, again, nil); err != nil {
		t.Fatal(err)
	}
	if got := inputState(t, store, "roads"); got.Digest != first.Digest || !got.IngestedAt.Equal(past) {
		t.Errorf("identical re-ingest = %+v, want digest %s and ingested_at %v kept", got, first.Digest, past)
	}

	for name, mutate := range map[string]func(*Feature){
		"geometry": func(f *Feature) { f.GeometryJSON = geomA },
		"name":     func(f *Feature) { f.Name = "renamed" },
		"tags":     func(f *Feature) { f.Tags = map[string]string{"highway": "primary"} },
		"source":   func(f *Feature) { f.SourceAPI = "arcgis" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := slices.Clone(base)
			mutate(&changed[1])
			if err := store.UpsertFeatures(ctx, rt, changed, nil); err != nil {
				t.Fatal(err)
			}
			got := inputState(t, store, "roads")
			if got.Digest == first.Digest || got.IngestedAt.Equal(past) {
				t.Errorf("changed %s = %+v, want a new digest and ingested_at", name, got)
			}
			// Restore for the next case.
			if err := store.UpsertFeatures(ctx, rt, base, nil); err != nil {
				t.Fatal(err)
			}
			if got := inputState(t, store, "roads"); got.Digest != first.Digest {
				t.Errorf("restored digest = %s, want %s (the digest is a function of the rows)", got.Digest, first.Digest)
			}
			past = backdateInputs(t, root)
		})
	}
}

// TestUpsertFeatures_DigestCoversEveryStoredSource: a source-scoped replace
// digests every stored row of the type, not just the source it replaced, and
// other types are untouched.
func TestUpsertFeatures_DigestCoversEveryStoredSource(t *testing.T) {
	ctx := context.Background()
	_, store, _ := openInputsStore(t)
	rt := resource.TypeRoads
	both := []Feature{feature("a1", "arcgis", geomA), feature("o1", "overpass", geomB)}
	if err := store.UpsertFeatures(ctx, rt, both, nil); err != nil {
		t.Fatal(err)
	}
	whole := inputState(t, store, "roads").Digest

	// Replacing only overpass with the same row: the arcgis row is still
	// there, so the digest is the same as for both.
	if err := store.UpsertFeatures(ctx, rt, both[1:], []string{"overpass"}); err != nil {
		t.Fatal(err)
	}
	if got := inputState(t, store, "roads").Digest; got != whole {
		t.Errorf("source-scoped identical replace digest = %s, want %s", got, whole)
	}
	// Dropping overpass's rows leaves arcgis's: a digest of one row, the same
	// as a store holding only that row.
	if err := store.UpsertFeatures(ctx, rt, nil, []string{"overpass"}); err != nil {
		t.Fatal(err)
	}
	_, only, _ := openInputsStore(t)
	if err := only.UpsertFeatures(ctx, rt, both[:1], nil); err != nil {
		t.Fatal(err)
	}
	if got, want := inputState(t, store, "roads").Digest, inputState(t, only, "roads").Digest; got != want || got == whole {
		t.Errorf("after dropping overpass digest = %s, want %s (arcgis alone)", got, want)
	}
	if got, err := store.InputDigests(ctx, "parking"); err != nil || got["parking"] != "" {
		t.Errorf("parking digest = %v, %v; want \"\" (never ingested)", got, err)
	}
}

// TestUpsertFeatures_NoRowsDigestIsEmpty: a type with no rows has digest "",
// and emptying a type is a change.
func TestUpsertFeatures_NoRowsDigestIsEmpty(t *testing.T) {
	ctx := context.Background()
	root, store, _ := openInputsStore(t)
	if err := store.UpsertFeatures(ctx, resource.TypeRoads, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := inputState(t, store, "roads"); got.Digest != "" {
		t.Errorf("empty ingest digest = %q, want \"\"", got.Digest)
	}
	if err := store.UpsertFeatures(ctx, resource.TypeRoads, []Feature{feature("w1", "overpass", geomA)}, nil); err != nil {
		t.Fatal(err)
	}
	past := backdateInputs(t, root)
	if err := store.UpsertFeatures(ctx, resource.TypeRoads, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := inputState(t, store, "roads"); got.Digest != "" || got.IngestedAt.Equal(past) {
		t.Errorf("emptied roads = %+v, want digest \"\" and a new ingested_at", got)
	}
}

// TestSaveBoundary_Digest: the boundary input follows its geometry.
func TestSaveBoundary_Digest(t *testing.T) {
	ctx := context.Background()
	root, store, _ := openInputsStore(t)
	if err := store.SaveBoundary(ctx, geomA, "nominatim"); err != nil {
		t.Fatal(err)
	}
	first := inputState(t, store, InputBoundary)
	if first.Digest == "" {
		t.Fatal("boundary digest is empty")
	}
	past := backdateInputs(t, root)
	if err := store.SaveBoundary(ctx, geomA, "nominatim"); err != nil {
		t.Fatal(err)
	}
	if got := inputState(t, store, InputBoundary); got.Digest != first.Digest || !got.IngestedAt.Equal(past) {
		t.Errorf("identical boundary = %+v, want %s at %v kept", got, first.Digest, past)
	}
	if err := store.SaveBoundary(ctx, geomB, "nominatim"); err != nil {
		t.Fatal(err)
	}
	if got := inputState(t, store, InputBoundary); got.Digest == first.Digest || got.IngestedAt.Equal(past) {
		t.Errorf("changed boundary = %+v, want a new digest and ingested_at", got)
	}
}

// TestGCSweep_RefreshesDigest: sweeping a stale source's features updates
// the type's digest to what is left; a dry run (GCScan) does not.
func TestGCSweep_RefreshesDigest(t *testing.T) {
	ctx := context.Background()
	_, store, _ := openInputsStore(t)
	rt := resource.TypeRoads
	both := []Feature{feature("a1", "arcgis", geomA), feature("o1", "overpass", geomB)}
	if err := store.UpsertFeatures(ctx, rt, both, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFeatures(ctx, resource.TypeParking, []Feature{feature("p1", "overpass", geomA)}, nil); err != nil {
		t.Fatal(err)
	}
	before, _ := store.InputDigests(ctx, "roads", "parking")
	if _, err := store.GCScan(ctx, []string{"overpass"}, testEpoch); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.InputDigests(ctx, "roads", "parking"); !maps.Equal(got, before) {
		t.Errorf("GCScan changed digests: %v -> %v", before, got)
	}
	report, err := store.GCSweep(ctx, []string{"overpass"}, testEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if report.StaleFeatures != 1 {
		t.Fatalf("swept %d stale features, want 1 (the arcgis road)", report.StaleFeatures)
	}
	_, only, _ := openInputsStore(t)
	if err := only.UpsertFeatures(ctx, rt, both[1:], nil); err != nil {
		t.Fatal(err)
	}
	after, _ := store.InputDigests(ctx, "roads", "parking")
	if want := inputState(t, only, "roads").Digest; after["roads"] != want {
		t.Errorf("roads digest after sweep = %s, want %s (overpass alone)", after["roads"], want)
	}
	if after["parking"] != before["parking"] {
		t.Errorf("parking digest moved (%s -> %s) though gc deleted none of it", before["parking"], after["parking"])
	}
}

// TestReadProducerInputs: each producer records exactly its inputs, read from
// the current digests.
func TestReadProducerInputs(t *testing.T) {
	ctx := context.Background()
	_, store, _ := openInputsStore(t)
	if err := store.SaveBoundary(ctx, geomA, "nominatim"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFeatures(ctx, resource.TypeRoads, []Feature{feature("w1", "overpass", geomA)}, nil); err != nil {
		t.Fatal(err)
	}
	digests, _ := store.InputDigests(ctx, InputBoundary, "roads")
	for producer, want := range map[string][]string{
		"roads":        {"boundary", "roads"},
		"parking":      {"boundary", "parking"},
		"sidewalks":    {"boundary", "sidewalks"},
		"combined":     {"boundary", "parking", "roads", "sidewalks"},
		"service:fire": {"boundary", "facility-fire", "roads", "routing-roads"},
		"census":       {"boundary", "layer-census-blocks"},
	} {
		got, err := ReadProducerInputs(ctx, store, producer)
		if err != nil {
			t.Fatalf("%s: %v", producer, err)
		}
		if keys := slices.Sorted(maps.Keys(got)); !slices.Equal(keys, want) {
			t.Errorf("%s inputs = %v, want %v", producer, keys, want)
		}
		if got[InputBoundary] != digests[InputBoundary] {
			t.Errorf("%s boundary digest = %q, want %q", producer, got[InputBoundary], digests[InputBoundary])
		}
		if d, ok := got["roads"]; ok && d != digests["roads"] {
			t.Errorf("%s roads digest = %q, want %q", producer, d, digests["roads"])
		}
	}
	if _, err := ReadProducerInputs(ctx, store, "nope"); err == nil {
		t.Error("an unknown producer read inputs; want an error")
	}
}

// TestCreateRun_RecordsInputs: the digests passed with a run are stored with
// it, and go when it is discarded.
func TestCreateRun_RecordsInputs(t *testing.T) {
	ctx := context.Background()
	root, store, _ := openInputsStore(t)
	in := map[string]string{"boundary": "b1", "roads": ""}
	run, err := store.CreateRun(ctx, "roads", "h", in)
	if err != nil {
		t.Fatal(err)
	}
	if got := runInputs(t, root, run.ID); !maps.Equal(got, in) {
		t.Errorf("run_inputs = %v, want %v", got, in)
	}
	if err := store.DiscardRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, root, `SELECT COUNT(*) FROM run_inputs WHERE run_id = ?`, run.ID); n != 0 {
		t.Errorf("%d run_inputs rows survived their run", n)
	}
}

// workingParts reads the working state for (city, hash) as part -> run.
func workingPartsOf(t *testing.T, root *RootStore, cityID int64, hash string) map[string]int64 {
	t.Helper()
	rows, err := root.db.QueryContext(context.Background(),
		`SELECT part, run_id FROM working_parts WHERE city_id = ? AND config_hash = ?`, cityID, hash)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int64{}
	for rows.Next() {
		var part string
		var run int64
		if err := rows.Scan(&part, &run); err != nil {
			t.Fatal(err)
		}
		out[part] = run
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// runInputs reads the digests recorded with a run.
func runInputs(t *testing.T, root *RootStore, runID int64) map[string]string {
	t.Helper()
	rows, err := root.db.QueryContext(context.Background(), `SELECT input, digest FROM run_inputs WHERE run_id = ?`, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestCommitLatest_SetsWorkingParts: a commit applies its parts to the
// latest compute (working_parts) for its hash, drops included.
func TestCommitLatest_SetsWorkingParts(t *testing.T) {
	ctx := context.Background()
	root, store, id := openInputsStore(t)
	roads, _ := commitRun(t, store, "h", "roads", nil)
	fire, _ := commitRun(t, store, "h", "service:fire", nil)
	if got, want := workingPartsOf(t, root, id, "h"), map[string]int64{"roads": roads, "service:fire": fire}; !maps.Equal(got, want) {
		t.Errorf("working state = %v, want %v", got, want)
	}
	if err := store.CommitLatest(ctx, "h", testEpoch, map[string]int64{"service:fire": 0}); err != nil {
		t.Fatal(err)
	}
	if got, want := workingPartsOf(t, root, id, "h"), map[string]int64{"roads": roads}; !maps.Equal(got, want) {
		t.Errorf("working state after the drop = %v, want %v", got, want)
	}
	if got := workingPartsOf(t, root, id, "other"); len(got) != 0 {
		t.Errorf("another hash's working state = %v, want empty", got)
	}
}

// TestGC_HonoursWorkingState: a run only the working state references is
// neither discarded nor swept as an orphan.
func TestGC_HonoursWorkingState(t *testing.T) {
	ctx := context.Background()
	root, store, _ := openInputsStore(t)
	run, snap := commitRun(t, store, "h", "roads", roadsRun(t, store, 100))
	// Delete the snapshot's manifest behind the store's back, leaving the run
	// referenced only by the working state, and age the run past the grace.
	for _, q := range []string{`DELETE FROM snapshot_parts WHERE snapshot_id = ?`, `DELETE FROM snapshots WHERE id = ?`} {
		if _, err := root.db.ExecContext(ctx, q, snap); err != nil {
			t.Fatal(err)
		}
	}
	stale := time.Now().Add(-2 * OrphanRunGrace).Unix()
	if _, err := root.db.ExecContext(ctx, `UPDATE runs SET computed_at = ? WHERE id = ?`, stale, run); err != nil {
		t.Fatal(err)
	}
	if err := store.DiscardRun(ctx, run); err == nil {
		t.Error("DiscardRun removed a run the working state references")
	}
	report, err := store.GCSweep(ctx, nil, testEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if report.OrphanRuns != 0 {
		t.Errorf("gc swept %d runs, want 0 (the working state references it)", report.OrphanRuns)
	}
	if n := countRows(t, root, `SELECT COUNT(*) FROM runs WHERE id = ?`, run); n != 1 {
		t.Error("the working state's run is gone")
	}
}

// TestSnapshot_FreezesEdgeAndBoundary: every new snapshot records its hex
// edge and the boundary in force, one stored copy per distinct boundary, and
// a frozen boundary goes once no snapshot uses it.
func TestSnapshot_FreezesEdgeAndBoundary(t *testing.T) {
	ctx := context.Background()
	root, store, id := openInputsStore(t)
	if err := store.SaveBoundary(ctx, geomA, "nominatim"); err != nil {
		t.Fatal(err)
	}
	_, s1 := commitRun(t, store, "h", "roads", nil)
	_, s2 := commitRun(t, store, "h", "parking", nil)
	if err := store.SaveBoundary(ctx, geomB, "nominatim"); err != nil {
		t.Fatal(err)
	}
	_, s3 := commitRun(t, store, "h", "sidewalks", nil)

	boundaryOf := func(snap int64) string {
		t.Helper()
		var gj string
		if err := root.db.QueryRowContext(ctx, `SELECT b.geometry_json FROM snapshots s
			JOIN snapshot_boundaries b ON b.id = s.boundary_id WHERE s.id = ?`, snap).Scan(&gj); err != nil {
			t.Fatalf("boundary of snapshot %d: %v", snap, err)
		}
		return gj
	}
	if boundaryOf(s1) != geomA || boundaryOf(s2) != geomA || boundaryOf(s3) != geomB {
		t.Errorf("frozen boundaries = %s, %s, %s; want geomA twice, then geomB", boundaryOf(s1), boundaryOf(s2), boundaryOf(s3))
	}
	if n := countRows(t, root, `SELECT COUNT(*) FROM snapshot_boundaries WHERE city_id = ?`, id); n != 2 {
		t.Errorf("%d frozen boundaries, want 2 (identical ones are shared)", n)
	}
	snaps, err := store.ListSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snaps {
		if s.HexEdgeM != testEdge {
			t.Errorf("snapshot %d hex_edge_m = %v, want %v", s.ID, s.HexEdgeM, testEdge)
		}
	}

	for _, snap := range []int64{s1, s2} {
		if _, err := store.DeleteSnapshot(ctx, snap); err != nil {
			t.Fatal(err)
		}
		// A survives s1's deletion (s2 still uses it), not s2's.
		wantA := 1
		if snap == s2 {
			wantA = 0
		}
		if n := countRows(t, root, `SELECT COUNT(*) FROM snapshot_boundaries WHERE geometry_json = ?`, geomA); n != wantA {
			t.Errorf("after deleting snapshot %d boundary A rows = %d, want %d", snap, n, wantA)
		}
	}
	if boundaryOf(s3) != geomB {
		t.Error("deleting other snapshots lost s3's boundary")
	}
	if _, _, err := store.CreateSnapshot(ctx, "h", 0, nil); err == nil {
		t.Error("a snapshot with no hex edge succeeded")
	}
}
