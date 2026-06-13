package geo

import (
	"context"
	"math"
	"testing"

	"github.com/peterstace/simplefeatures/geom"
)

// These goldens pin the geometry compute hot path so later refactors
// (clip-first union, hex pruning, buffer parallelism, cohort single-pass)
// can be verified to not change area numbers. The existing forecast goldens
// in internal/export pin downstream math through a MockStore; nothing pins
// ClipHexesToBoundary / ComputeHexStats / hexCoverageArea. These do.
//
// Epsilon choice: areas here are O(10^3) to O(10^4) sqm. The simplefeatures
// overlay engine is exact-rational for these axis-aligned inputs, so the
// numbers come back clean, but UnionMany ordering and float accumulation can
// perturb the last ulps. 1e-6 absolute is ~13 orders of magnitude tighter
// than the smallest area asserted (>1000 sqm) — comfortably within "the area
// did not change" while immune to ulp noise. We use absolute, not relative,
// because every asserted magnitude is well above 1.
const areaEps = 1e-6

func approx(t *testing.T, got, want float64, label string) {
	t.Helper()
	if math.Abs(got-want) > areaEps {
		t.Errorf("%s = %.9f, want %.9f (|diff| = %.3g > eps %g)",
			label, got, want, math.Abs(got-want), areaEps)
	}
}

// rectHex wraps an axis-aligned rectangle as a Hex. The compute path is
// geometry-agnostic (it only calls Envelope/Area/Intersection), so using
// rectangles instead of true hexagons keeps every expected area hand-derivable
// from width*height while exercising the identical code.
func rectHex(id string, x1, y1, x2, y2 float64) Hex {
	return Hex{
		ID:      id,
		CenterX: (x1 + x2) / 2,
		CenterY: (y1 + y2) / 2,
		Geom:    makeRect(x1, y1, x2, y2),
	}
}

// TestComputeHexStats_Golden pins per-hex Area and PctCovered for a fixed
// 2x2 grid of 100x100 (10000 sqm) cells against two overlapping feature
// rectangles, after first clipping the cells to a boundary.
//
// Fixed inputs (projected units = meters):
//
//	Cells (each 100x100 = 10000 sqm):
//	  A = [  0,  0,100,100]   B = [100,  0,200,100]
//	  C = [  0,100,100,200]   D = [100,100,200,200]
//
//	Features (buffered "roads", deliberately overlapping):
//	  horizontal = [ 0, 40,200, 60]   (a 200x20 east-west corridor)
//	  vertical   = [40,  0, 60,200]   (a  20x200 north-south corridor)
//	  Their union double-covers the 20x20 patch at [40,40,60,60] (and at
//	  [40,140,60,160] etc.) — that overlap must be counted ONCE.
//
//	Boundary = [25,25,175,175]. This clips:
//	  A -> [25,25,100,100]  (75x75)
//	  B -> [100,25,175,100] (75x75)
//	  C -> [25,100,100,175] (75x75)
//	  D -> [100,100,175,175](75x75)
//
// Derivations below the test compute each expected coverage area by hand.
func TestComputeHexStats_Golden(t *testing.T) {
	cells := []Hex{
		rectHex("A", 0, 0, 100, 100),
		rectHex("B", 100, 0, 200, 100),
		rectHex("C", 0, 100, 100, 200),
		rectHex("D", 100, 100, 200, 200),
	}
	horizontal := makeRect(0, 40, 200, 60)
	vertical := makeRect(40, 0, 60, 200)
	idx := NewGeomIndexFromGeoms([]geom.Geometry{horizontal, vertical})
	boundary := makeRect(25, 25, 175, 175)

	clipped := ClipHexesToBoundary(context.Background(), cells, boundary, nil)
	if len(clipped) != 4 {
		t.Fatalf("expected all 4 cells to survive clipping, got %d", len(clipped))
	}
	stats := ComputeHexStats(context.Background(), clipped, idx, "roads", nil)

	got := make(map[string]HexStat, len(stats))
	for _, s := range stats {
		got[s.HexID] = s
	}

	// Each clipped cell is 75x75 = 5625 sqm (its Area for PctCovered base).
	const clippedHexArea = 75.0 * 75.0 // 5625

	// --- Cell A, clipped to [25,25,100,100] ---
	// Covered = union(horizontal ∩ cell, vertical ∩ cell).
	//   horizontal [0,40,200,60] ∩ [25,25,100,100] = [25,40,100,60] -> 75*20=1500
	//   vertical   [40,0,60,200] ∩ [25,25,100,100] = [40,25,60,100] -> 20*75=1500
	//   overlap (both) = [40,40,60,60] -> 20*20 = 400, counted once.
	//   union = 1500 + 1500 - 400 = 2600
	wantA := 2600.0
	approx(t, got["A"].Area, wantA, "A.Area")
	approx(t, got["A"].PctCovered, wantA/clippedHexArea*100, "A.PctCovered")

	// --- Cell B, clipped to [100,25,175,100] ---
	// vertical's x-range is [40,60], entirely left of x=100, so it does NOT
	// intersect cell B. Only the horizontal corridor covers B.
	//   horizontal [0,40,200,60] ∩ [100,25,175,100] = [100,40,175,60] -> 75*20=1500
	//   union = 1500 (no overlap to dedupe)
	wantB := 1500.0
	approx(t, got["B"].Area, wantB, "B.Area")
	approx(t, got["B"].PctCovered, wantB/clippedHexArea*100, "B.PctCovered")

	// --- Cell C, clipped to [25,100,100,175] ---
	// horizontal's y-range is [40,60], entirely below y=100, so it does NOT
	// intersect cell C. Only the vertical corridor covers C.
	//   vertical [40,0,60,200] ∩ [25,100,100,175] = [40,100,60,175] -> 20*75=1500
	wantC := 1500.0
	approx(t, got["C"].Area, wantC, "C.Area")
	approx(t, got["C"].PctCovered, wantC/clippedHexArea*100, "C.PctCovered")

	// --- Cell D, clipped to [100,100,175,175] ---
	// Neither corridor reaches this cell: horizontal y in [40,60] (<100),
	// vertical x in [40,60] (<100). No coverage -> hex dropped (area<=0).
	if _, ok := got["D"]; ok {
		t.Errorf("expected cell D to have no coverage, got %+v", got["D"])
	}
}

// TestHexCoverageArea_ClipFirstIdentity asserts the SET IDENTITY that later
// batches rely on: union-first (current impl) and clip-first produce the same
// covered area. Both must dedupe overlap and ignore empty intersections.
//
//	union-first (current): UnionMany(candidates) then Intersection(hex), Area.
//	clip-first (reference): Intersection(hex, cand_i) per candidate, keep the
//	  polygonal parts, UnionMany those, Area.
//
// They are equal by distributivity of ∩ over ∪:
//
//	hex ∩ (c1 ∪ c2 ∪ ...) = (hex∩c1) ∪ (hex∩c2) ∪ ...
func TestHexCoverageArea_ClipFirstIdentity(t *testing.T) {
	hex := makeRect(0, 0, 100, 100)

	cases := []struct {
		name       string
		candidates []geom.Geometry
	}{
		{
			// Two candidates overlapping INSIDE the hex: the [40,40,60,60]
			// patch is shared and must be counted once by both methods.
			name: "overlap_inside_hex",
			candidates: []geom.Geometry{
				makeRect(0, 40, 100, 60),
				makeRect(40, 0, 60, 100),
			},
		},
		{
			// Candidate crossing the hex boundary: only the in-hex part counts.
			name: "candidate_crosses_boundary",
			candidates: []geom.Geometry{
				makeRect(50, 50, 200, 70),
			},
		},
		{
			// One real candidate plus one whose intersection with the hex is
			// empty (lies entirely outside): the empty one must not change the
			// result under either method.
			name: "one_candidate_empty_intersection",
			candidates: []geom.Geometry{
				makeRect(10, 10, 30, 30),
				makeRect(500, 500, 600, 600),
			},
		},
		{
			// Three mutually overlapping candidates: stress the dedupe across
			// more than a pairwise overlap.
			name: "triple_overlap",
			candidates: []geom.Geometry{
				makeRect(10, 10, 60, 60),
				makeRect(40, 40, 90, 90),
				makeRect(30, 30, 70, 70),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unionFirst, ok := hexCoverageArea(hex, tc.candidates)
			if !ok {
				// ok==false means "no coverage"; the reference must agree.
				if a := clipFirstArea(t, hex, tc.candidates); a > areaEps {
					t.Fatalf("union-first reported no coverage but clip-first = %.9f", a)
				}
				return
			}
			clipFirst := clipFirstArea(t, hex, tc.candidates)
			approx(t, clipFirst, unionFirst, tc.name+": clip-first vs union-first")
		})
	}
}

// clipFirstArea is the reference implementation of the clip-first computation
// used purely to validate the union-first production code. It intersects the
// hex with each candidate, retains only the polygonal parts, unions those
// fragments, and returns the area.
func clipFirstArea(t *testing.T, hex geom.Geometry, candidates []geom.Geometry) float64 {
	t.Helper()
	var parts []geom.Geometry
	for _, c := range candidates {
		inter, err := geom.Intersection(hex, c)
		if err != nil {
			t.Fatalf("intersection: %v", err)
		}
		if inter.IsEmpty() {
			continue
		}
		poly, err := RetainPolygonal(inter)
		if err != nil {
			t.Fatalf("retain polygonal: %v", err)
		}
		if poly.IsEmpty() {
			continue
		}
		parts = append(parts, poly)
	}
	if len(parts) == 0 {
		return 0
	}
	if len(parts) == 1 {
		return parts[0].Area()
	}
	u, err := geom.UnionMany(parts)
	if err != nil {
		t.Fatalf("union many: %v", err)
	}
	return u.Area()
}
