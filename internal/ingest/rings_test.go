package ingest

import "testing"

// A single open chain whose two ends sit within boundaryCloseTolDeg of each
// other (the Denver case: ~10 m gap where adjacent member ways don't share an
// exact node). Exact stitching drops it; tolerant stitching snaps it shut.
func TestStitchRingsBidi_SnapsNearClosedGap(t *testing.T) {
	// Square with the final vertex offset from the start by 5e-4° (~55 m),
	// inside the 1e-3° tolerance.
	ways := []stitchInput{
		{id: 1, coords: [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}, {0, 0.0005}}},
	}

	// Exact closure (closeTol=0) drops the ring.
	if rings, dropped := stitchRings(ways); len(rings) != 0 || len(dropped) != 1 {
		t.Fatalf("exact: expected 0 rings / 1 dropped, got %d rings / %v dropped", len(rings), dropped)
	}

	// Tolerant closure recovers it.
	rings, dropped := stitchRingsBidi(ways)
	if len(rings) != 1 {
		t.Fatalf("tolerant: expected 1 ring, got %d", len(rings))
	}
	if len(dropped) != 0 {
		t.Errorf("tolerant: expected 0 dropped, got %v", dropped)
	}
	if !isClosedRing(rings[0]) {
		t.Errorf("tolerant: ring not closed: %v", rings[0])
	}
}

// A gap larger than the tolerance must still drop — tolerant closure only
// recovers genuine near-misses, not arbitrarily broken relations.
func TestStitchRingsBidi_LargeGapStillDropped(t *testing.T) {
	ways := []stitchInput{
		{id: 1, coords: [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}, {0, 0.05}}}, // ~5.5 km gap
	}
	rings, dropped := stitchRingsBidi(ways)
	if len(rings) != 0 || len(dropped) != 1 {
		t.Fatalf("expected 0 rings / 1 dropped, got %d rings / %v dropped", len(rings), dropped)
	}
}

// Tolerant closure must never merge two distinct chains: a relation with two
// separate near-closed rings yields two rings, each snapped on its own ends.
func TestStitchRingsBidi_DoesNotMergeDistinctChains(t *testing.T) {
	ways := []stitchInput{
		{id: 1, coords: [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}, {0, 0.0005}}},
		{id: 2, coords: [][2]float64{{5, 5}, {6, 5}, {6, 6}, {5, 6}, {5, 5.0005}}},
	}
	rings, dropped := stitchRingsBidi(ways)
	if len(rings) != 2 {
		t.Fatalf("expected 2 rings, got %d (dropped %v)", len(rings), dropped)
	}
	for _, r := range rings {
		if !isClosedRing(r) {
			t.Errorf("ring not closed: %v", r)
		}
	}
}
