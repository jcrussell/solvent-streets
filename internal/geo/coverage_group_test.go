package geo

import (
	"math"
	"testing"

	"github.com/peterstace/simplefeatures/geom"
)

// computeByGroupPerClass reproduces the OLD per-class cohort approach
// (resource.ComputeRoadCohortAreas before the single-pass rewrite): build a
// classGeoms map keyed by every distinct label, and for each such label build
// an index over only that label's geoms, run ComputeHexStats over the full
// grid, and set areas[label] = sum. Crucially it assigns areas[label] for
// EVERY distinct label, so a label whose geoms cover no hex yields a 0.0 key
// rather than being absent — exactly what the old loop did by keying off every
// class in classGeoms. This is the reference the single-pass
// ComputeHexCoverageByGroup must match exactly, including the zero keys.
func computeByGroupPerClass(t *testing.T, hexes []Hex, geoms []geom.Geometry, labels []string) map[string]float64 {
	t.Helper()
	byLabel := make(map[string][]geom.Geometry)
	for i, g := range geoms {
		byLabel[labels[i]] = append(byLabel[labels[i]], g)
	}
	areas := make(map[string]float64)
	for label, lg := range byLabel {
		idx := NewGeomIndexFromGeoms(lg)
		stats := ComputeHexStats(t.Context(), hexes, idx, label, nil)
		var sum float64
		for _, s := range stats {
			sum += s.Area
		}
		// Set unconditionally (even sum == 0) so the reference key set is "one
		// key per distinct label", matching the old ComputeRoadCohortAreas.
		areas[label] = sum
	}
	return areas
}

func assertMapEqual(t *testing.T, got, want map[string]float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("label sets differ: got %v, want %v", got, want)
	}
	for label, w := range want {
		g, ok := got[label]
		if !ok {
			t.Errorf("missing label %q in single-pass result", label)
			continue
		}
		if math.Abs(g-w) > 1e-6 {
			t.Errorf("label %q: single-pass=%v per-class=%v (diff %v)", label, g, w, math.Abs(g-w))
		}
	}
}

// TestComputeHexCoverageByGroup_EqualsPerClass proves the single-pass cohort
// computation is EXACTLY equivalent to the old per-class approach (one index +
// ComputeHexStats per class). The fixture deliberately includes:
//   - same-class overlap inside hexes: two "residential" features overlap, so
//     the shared area must be dedup'd and counted once for residential.
//   - cross-class overlap inside hexes: a "primary" feature overlaps the
//     residential ones, and that shared area must be counted separately under
//     BOTH classes (each class is computed against its own clipped fragments).
//   - features spanning multiple hexes, so the per-hex clip-and-accumulate path
//     is exercised across many hexes.
func TestComputeHexCoverageByGroup_EqualsPerClass(t *testing.T) {
	hexes := HexGrid(0, 0, 100, 100, 20)
	if len(hexes) == 0 {
		t.Fatal("hex grid is empty")
	}

	var geoms []geom.Geometry
	var labels []string
	add := func(g geom.Geometry, label string) {
		geoms = append(geoms, g)
		labels = append(labels, label)
	}

	// Two residential features that overlap each other (same-class overlap).
	// Their union covers [10,60]x[30,70]; the [30,40] strip is shared and must
	// be counted once for residential.
	add(makeRect(10, 30, 40, 70), "residential")
	add(makeRect(30, 30, 60, 70), "residential")

	// A primary feature that overlaps the residential block (cross-class
	// overlap) in [30,60]x[40,60]. That overlap is counted under primary AND
	// under residential, independently.
	add(makeRect(30, 40, 80, 60), "primary")

	// A secondary feature in a different region, spanning several hexes, with
	// no overlap with the others.
	add(makeRect(10, 80, 90, 95), "secondary")

	// A second primary feature that overlaps the first primary (same-class
	// overlap, primary), sharing [70,80]x[40,60].
	add(makeRect(70, 40, 95, 60), "primary")

	// A class whose only feature falls ENTIRELY OFF the hex grid (far in the
	// negative quadrant, well outside [0,100]x[0,100]). It covers zero clipped
	// hexes, mirroring a real cohort whose features sit in the bbox margin but
	// outside the boundary. The old per-class loop still produced a key for it
	// (areas[class] = 0); the single-pass result must do the same, so this
	// class must appear with value 0.0 and old==new must hold including it.
	add(makeRect(-500, -500, -400, -400), "offgrid")

	got := ComputeHexCoverageByGroup(t.Context(), hexes, geoms, labels, nil)
	want := computeByGroupPerClass(t, hexes, geoms, labels)

	assertMapEqual(t, got, want)

	// The zero-coverage class must be PRESENT with value 0.0, not dropped.
	zero, ok := got["offgrid"]
	if !ok {
		t.Errorf("zero-coverage class %q dropped from single-pass result; want a 0.0 key", "offgrid")
	} else if math.Abs(zero) > 1e-6 {
		t.Errorf("zero-coverage class %q: got %v, want 0.0", "offgrid", zero)
	}
	// And the reference (old per-class) result must also carry the zero key, so
	// the assertMapEqual above is genuinely comparing key sets that include it.
	if _, ok := want["offgrid"]; !ok {
		t.Errorf("reference per-class result missing zero key %q; old==new cannot hold", "offgrid")
	}

	// Sanity: every class present and positive (so the equality above is not
	// trivially comparing empty maps).
	for _, label := range []string{"residential", "primary", "secondary"} {
		if got[label] <= 0 {
			t.Errorf("class %q: expected positive area, got %v", label, got[label])
		}
	}

	// Sanity: same-class overlap was dedup'd. The two residential rects each
	// have area 30*40=1200; naive double-count would be 2400, but their union
	// (clipped to the grid covering [10,60]x[30,70]) is 50*40=2000. The cohort
	// value must be <= the union area, well below the naive sum.
	if got["residential"] > 2000+1e-6 {
		t.Errorf("residential area %v exceeds dedup'd union area 2000 — overlap not dedup'd", got["residential"])
	}
}
