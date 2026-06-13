package geo

import (
	"context"
	"fmt"
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

// clipExhaustiveReference clips every hex against the boundary the OLD,
// unpruned way: for each hex, intersect with the whole boundary, retain the
// polygonal parts, and keep the survivor. It is the from-scratch reference the
// pruned ClipHexesToBoundary must match exactly. Returns survivors keyed by ID
// and the total kept area.
func clipExhaustiveReference(t *testing.T, hexes []Hex, boundary geom.Geometry) (map[string]float64, float64) {
	t.Helper()
	out := make(map[string]float64)
	var total float64
	for _, h := range hexes {
		inter, err := geom.Intersection(h.Geom, boundary)
		if err != nil {
			t.Fatalf("reference intersection for %s: %v", h.ID, err)
		}
		if inter.IsEmpty() {
			continue
		}
		poly, err := RetainPolygonal(inter)
		if err != nil {
			t.Fatalf("reference retain for %s: %v", h.ID, err)
		}
		if poly.IsEmpty() {
			continue
		}
		a := poly.Area()
		if a <= 0 {
			continue
		}
		out[h.ID] = a
		total += a
	}
	return out, total
}

// notchedBoundary is a 300x300 square with a 100x100 rectangular bite removed
// from its top-right, yielding an L-shape (a concave polygon). Built as a
// single exterior ring so a hex whose envelope spans the notch sees the
// concave edges in its envelope and must take the full-intersection path.
//
//	(0,300)            (200,300)
//	   +------------------+
//	   |                  |
//	   |        notch ->  +-----------+ (300,200)
//	   |                  (200,200)   |
//	   |                              |
//	   +------------------------------+
//	(0,0)                          (300,0)
func notchedBoundary() geom.Geometry {
	coords := []float64{
		0, 0,
		300, 0,
		300, 200,
		200, 200,
		200, 300,
		0, 300,
		0, 0,
	}
	ring := geom.NewLineString(geom.NewSequence(coords, geom.DimXY))
	return geom.NewPolygon([]geom.LineString{ring}).AsGeometry()
}

// TestClipHexesToBoundary_ConcaveEqualsExhaustive is the key safety net for the
// interior-hex pruning optimization: over a concave (L-shaped/notched)
// boundary, the pruned ClipHexesToBoundary must produce EXACTLY the same
// survivors and per-hex areas as a from-scratch full-intersection reference.
//
// It specifically exercises:
//   - hexes fully inside the L are kept (fast interior path, unclipped),
//   - hexes inside the removed notch are dropped (fast exterior path),
//   - hexes straddling the notch edges are clipped (full-intersection path).
func TestClipHexesToBoundary_ConcaveEqualsExhaustive(t *testing.T) {
	boundary := notchedBoundary()
	// 50-unit grid of rectangles, a few past each edge so some hexes are fully
	// exterior. The grid origin is at -75 (NOT -50) so that the cells are
	// OFFSET from the boundary lines by half a cell: the boundary edges at
	// x/y = 0, 200, 300 fall at the MIDPOINT of a 50-unit cell rather than on a
	// cell edge. That offset is load-bearing for this test's oracle. With an
	// origin of -50 the cell edges coincide with every boundary line, so the
	// clip never bisects a cell and ZERO partial-straddle hexes are produced —
	// the boundary always cleaves cleanly along shared edges. The test then
	// only exercises whole-keep / whole-drop and a mutation that disables
	// clipping entirely (route every hex through center classification) still
	// passes. Offsetting to -75 forces the boundary through cell interiors,
	// yielding 24 partial-straddle hexes (areas 625/1250/1875) so the
	// clip branch's area math is actually pinned. 8x8 = 64 cells over
	// [-75,275]x[-75,275].
	var hexes []Hex
	for cx := -75; cx < 300; cx += 50 {
		for cy := -75; cy < 300; cy += 50 {
			x1, y1 := float64(cx), float64(cy)
			hexes = append(hexes, rectHex(
				fmt.Sprintf("c:%d:%d", cx, cy), x1, y1, x1+50, y1+50))
		}
	}

	wantAreas, wantTotal := clipExhaustiveReference(t, hexes, boundary)

	// Guard the oracle itself: the offset grid MUST produce partially-clipped
	// hexes, otherwise the clip branch is untested (every survivor would be a
	// whole 2500-area cell and the reference would agree even if clipping were
	// disabled). Count survivors whose area is neither 0 nor a full cell.
	const wholeCell = 50.0 * 50.0 // 2500
	partials := 0
	for _, a := range wantAreas {
		if math.Abs(a-wholeCell) > areaEps {
			partials++
		}
	}
	if partials == 0 {
		t.Fatalf("oracle is weak: no partial-straddle hexes in fixture; "+
			"the clip branch is not exercised (survivors=%d)", len(wantAreas))
	}

	clipped := ClipHexesToBoundary(context.Background(), hexes, boundary, nil)
	gotAreas := make(map[string]float64, len(clipped))
	var gotTotal float64
	for _, h := range clipped {
		a := h.Geom.Area()
		gotAreas[h.ID] = a
		gotTotal += a
	}

	if len(gotAreas) != len(wantAreas) {
		t.Fatalf("survivor count = %d, want %d (pruned set != exhaustive set)",
			len(gotAreas), len(wantAreas))
	}
	for id, want := range wantAreas {
		got, ok := gotAreas[id]
		if !ok {
			t.Errorf("hex %s missing from pruned result (present in exhaustive)", id)
			continue
		}
		approx(t, got, want, "area["+id+"]")
	}
	approx(t, gotTotal, wantTotal, "total kept area: pruned vs exhaustive")

	// Sanity, branch by branch (independent of the exhaustive reference so the
	// clip branch is pinned by hand-derived values, not only by the oracle):
	//
	//   interior-keep-whole: c:25:25 is cell [25,75]x[25,75], wholly inside the
	//     L (below and left of the notch) -> kept unclipped at 2500.
	if a := gotAreas["c:25:25"]; math.Abs(a-2500) > areaEps {
		t.Errorf("interior hex c:25:25 area = %f, want 2500 (kept unclipped)", a)
	}
	//   exterior-drop: c:-75:-75 is cell [-75,-25]^2, wholly outside -> dropped.
	if a, ok := gotAreas["c:-75:-75"]; ok {
		t.Errorf("exterior hex c:-75:-75 should be dropped, got area %f", a)
	}
	//   notch-drop: c:225:225 is cell [225,275]^2, wholly inside the removed
	//     notch [200,300]^2 -> dropped.
	if a, ok := gotAreas["c:225:225"]; ok {
		t.Errorf("notch hex c:225:225 should be dropped, got area %f", a)
	}
	//   straddle-clip (KNOWN partial, hand-verified): c:-25:25 is cell
	//     [-25,25]x[25,75]. Its left half (x<0) is outside the boundary; the
	//     clip keeps [0,25]x[25,75] = 25*50 = 1250. This pins the clip branch's
	//     area math to an independent value, not just to the reference total.
	if a := gotAreas["c:-25:25"]; math.Abs(a-1250) > areaEps {
		t.Errorf("straddle hex c:-25:25 area = %f, want 1250 (clipped to [0,25]x[25,75])", a)
	}
}

// TestClipHexesToBoundary_CoincidentEdge guards the Part-1 case: a hex whose
// edge exactly coincides with a boundary edge. Intersection can then emit a
// mixed-dimension GeometryCollection (2-D overlap + 1-D shared edge) which
// geom.Union rejects (an error in simplefeatures v0.59.0; historically an
// uncatchable JTS panic inside a ParallelMap worker). clipHexToCandidates must
// RetainPolygonal first; here we assert no panic/error and a correct clip area.
func TestClipHexesToBoundary_CoincidentEdge(t *testing.T) {
	// Boundary right edge at x=100. The hex spans [50,150]x[0,100]; its left
	// portion [50,100]x[0,100] is inside, and its right edge of the overlap
	// (x=100) coincides with the boundary's right edge.
	boundary := makeRect(0, 0, 100, 200)
	hex := rectHex("straddle", 50, 0, 150, 100)

	clipped := ClipHexesToBoundary(context.Background(), []Hex{hex}, boundary, nil)
	if len(clipped) != 1 {
		t.Fatalf("expected 1 clipped hex, got %d", len(clipped))
	}
	// In-hex area = [50,100]x[0,100] = 50*100 = 5000.
	if a := clipped[0].Geom.Area(); math.Abs(a-5000) > areaEps {
		t.Errorf("clipped area = %f, want 5000", a)
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
