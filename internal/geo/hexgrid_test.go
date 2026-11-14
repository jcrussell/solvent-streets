package geo

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/peterstace/simplefeatures/geom"
)

func TestHexGrid_Count(t *testing.T) {
	// 1000m x 1000m area with 100m edge hexes
	hexes := HexGrid(0, 0, 1000, 1000, 100)
	// With 100m edge, hex width=200m, col spacing=150m, row spacing=173m
	// ~7 cols * ~6 rows = ~42 hexes, some clipped by envelope reject
	if len(hexes) < 20 || len(hexes) > 80 {
		t.Errorf("expected ~30-50 hexes for 1km^2 at 100m edge, got %d", len(hexes))
	}
}

func TestHexGrid_HexArea(t *testing.T) {
	hexes := HexGrid(0, 0, 500, 500, 100)
	if len(hexes) == 0 {
		t.Fatal("expected at least one hex")
	}
	// Area of regular hex with edge e: (3*sqrt(3)/2) * e^2
	expectedArea := 3 * math.Sqrt(3) / 2 * 100 * 100
	area := hexes[0].Geom.Area()
	if math.Abs(area-expectedArea)/expectedArea > 0.01 {
		t.Errorf("hex area = %f, want ~%f", area, expectedArea)
	}
}

func TestHexGrid_UniqueIDs(t *testing.T) {
	hexes := HexGrid(0, 0, 500, 500, 100)
	seen := make(map[string]bool)
	for _, h := range hexes {
		if seen[h.ID] {
			t.Errorf("duplicate hex ID: %s", h.ID)
		}
		seen[h.ID] = true
	}
}

func TestHexGrid_DegenerateInputsReturnNil(t *testing.T) {
	cases := []struct {
		name                   string
		minX, minY, maxX, maxY float64
		edge                   float64
	}{
		{"zero_edge", 0, 0, 100, 100, 0},
		{"negative_edge", 0, 0, 100, 100, -10},
		{"inverted_x", 100, 0, 0, 100, 10},
		{"inverted_y", 0, 100, 100, 0, 10},
		{"empty_x", 50, 0, 50, 100, 10},
		{"empty_y", 0, 50, 100, 50, 10},
		// The non-finite edges each need their own case: `edge <= 0` is false
		// for all three, so they slipped past the guard entirely and produced
		// one hex whose polygon was all NaN. That hex does NOT poison the
		// coverage math — its NaN envelope overlaps nothing, so the R-tree
		// search returns no candidates and ComputeHexStats returns early. The
		// outcome is a silently empty hex layer, i.e. a blank city.
		//
		// +Inf is the reachable one: resolveHexEdge gates on `> 0`, which is
		// false for NaN but true for +Inf, so a `hex_edge_m = inf` config
		// landed here before config grew its own finite guard.
		{"nan_edge", 0, 0, 1000, 1000, math.NaN()},
		{"pos_inf_edge", 0, 0, 1000, 1000, math.Inf(1)},
		{"neg_inf_edge", 0, 0, 1000, 1000, math.Inf(-1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HexGrid(tc.minX, tc.minY, tc.maxX, tc.maxY, tc.edge); got != nil {
				t.Errorf("expected nil for degenerate input, got %d hexes", len(got))
			}
		})
	}
}

func TestComputeHexCoverageByGroup_LenMismatchReturnsEmpty(t *testing.T) {
	// geoms and labels must be parallel; a mismatch would panic on labels[id].
	// The guard must return an empty (non-nil) map instead.
	hexes := HexGrid(0, 0, 100, 100, 50)
	geoms := []geom.Geometry{makeRect(0, 0, 50, 50), makeRect(50, 50, 100, 100)}
	labels := []string{"only-one"} // len 1 != len 2
	got := ComputeHexCoverageByGroup(context.Background(), hexes, geoms, labels, nil)
	if got == nil {
		t.Fatal("expected non-nil empty map, got nil")
	}
	if len(got) != 0 {
		t.Errorf("expected empty map for mismatched lengths, got %d entries", len(got))
	}
}

func TestComputeHexStats_NoIntersection(t *testing.T) {
	hexes := HexGrid(0, 0, 500, 500, 100)
	// Index a geometry far away
	idx := NewGeomIndexFromGeoms([]geom.Geometry{makeRect(10000, 10000, 10100, 10100)})
	stats := ComputeHexStats(context.Background(), hexes, idx, "roads", nil)
	if len(stats) != 0 {
		t.Errorf("expected 0 stats for non-intersecting, got %d", len(stats))
	}
}

func TestComputeHexStats_PartialIntersection(t *testing.T) {
	hexes := HexGrid(0, 0, 500, 500, 100)
	// Small rect that should intersect a few hexes
	idx := NewGeomIndexFromGeoms([]geom.Geometry{makeRect(100, 100, 200, 200)})
	stats := ComputeHexStats(context.Background(), hexes, idx, "roads", nil)
	if len(stats) == 0 {
		t.Error("expected some hex stats for intersecting geometry")
	}
	for _, s := range stats {
		if s.PctCovered <= 0 || s.PctCovered > 100 {
			t.Errorf("invalid pct_covered: %f", s.PctCovered)
		}
		if s.Area <= 0 {
			t.Errorf("expected positive area, got %f", s.Area)
		}
	}
}

func TestComputeHexStats_DedupesOverlappingCandidates(t *testing.T) {
	// Two buffered "roads" overlap heavily at a crossing point inside one hex.
	// A sum-of-intersections approach would double-count the overlap and
	// report area greater than the hex area; the per-hex local union must
	// collapse the overlap so area stays bounded by hex area.
	hexes := HexGrid(0, 0, 100, 100, 50)
	horizontal := makeRect(0, 40, 100, 60)
	vertical := makeRect(40, 0, 60, 100)
	idx := NewGeomIndexFromGeoms([]geom.Geometry{horizontal, vertical})
	stats := ComputeHexStats(context.Background(), hexes, idx, "roads", nil)
	if len(stats) == 0 {
		t.Fatal("expected at least one stat")
	}
	for _, s := range stats {
		if s.PctCovered > 100 {
			t.Errorf("pct_covered exceeded 100 despite clamp: %f", s.PctCovered)
		}
		// Overlap at the crossing is a 20x20 patch; summing intersections
		// naively would inflate area by ~400 sqm per hex that contains
		// both arms. Confirm we stay close to the true unioned area.
		hexArea := 3 * math.Sqrt(3) / 2 * 50 * 50
		if s.Area > hexArea {
			t.Errorf("area %f exceeds hex area %f — overlap not deduped", s.Area, hexArea)
		}
	}
}

func TestComputeHexStats_KeepsSubHundredSqMHexes(t *testing.T) {
	// A clipped boundary sliver hex (<100 sqm) must be kept: dropping it would
	// under-count the paved_share numerator while the denominator stays full.
	// The sliver filter lives in the display layer; aggregate stats include
	// slivers so numerator/denominator scope matches.
	tinyHex := Hex{ID: "tiny", Geom: makeRect(0, 0, 9, 9)} // 81 sqm
	idx := NewGeomIndexFromGeoms([]geom.Geometry{makeRect(0, 0, 5, 5)})
	stats := ComputeHexStats(context.Background(), []Hex{tinyHex}, idx, "roads", nil)
	if len(stats) != 1 {
		t.Fatalf("expected 1 stat for sub-100sqm hex, got %d", len(stats))
	}
	if stats[0].Area <= 0 {
		t.Errorf("expected positive area for sliver coverage, got %f", stats[0].Area)
	}
}

func TestClipHexesToBoundary(t *testing.T) {
	hexes := HexGrid(0, 0, 500, 500, 100)
	// Boundary covers only part of the hex grid
	boundary := makeRect(50, 50, 250, 250)
	clipped := ClipHexesToBoundary(context.Background(), hexes, boundary, nil)
	if len(clipped) == 0 {
		t.Error("expected some hexes after clipping")
	}
	if len(clipped) >= len(hexes) {
		t.Error("expected fewer hexes after clipping to smaller boundary")
	}
}

func BenchmarkComputeHexStats(b *testing.B) {
	// Realistic workload: grid of hexes + scattered rectangles as candidates.
	hexes := HexGrid(0, 0, 10000, 10000, 100) // ~4400 hexes
	var geoms []geom.Geometry
	for i := range 100 {
		x := float64(i%10) * 1000
		y := float64(i/10) * 1000
		geoms = append(geoms, makeRect(x, y, x+200, y+50))
	}
	idx := NewGeomIndexFromGeoms(geoms)
	b.ResetTimer()
	for range b.N {
		ComputeHexStats(context.Background(), hexes, idx, "roads", nil)
	}
}

func TestComputeHexStats_SkipsZeroAreaHex(t *testing.T) {
	// A degenerate hex whose Geom has a real envelope but zero area (a line):
	// candidates are found, but hexArea==0 would divide-by-zero. It must be
	// skipped, not emit an Inf/NaN pct.
	line, err := geom.UnmarshalWKT("LINESTRING(0 0, 200 200)")
	if err != nil {
		t.Fatalf("build line: %v", err)
	}
	hexes := []Hex{{ID: "hex:0:0", Geom: line}}
	idx := NewGeomIndexFromGeoms([]geom.Geometry{makeRect(0, 0, 200, 200)})
	stats := ComputeHexStats(context.Background(), hexes, idx, "roads", nil)
	if len(stats) != 0 {
		t.Errorf("expected zero-area hex to be skipped, got %d stats: %+v", len(stats), stats)
	}
}

// Without a usable boundary the lattice comes back whole, with an error the
// caller can tell apart (ErrNoClipBoundary) and warn about.
func TestClipHexesToBoundaryGeoJSON_NoUsableBoundary(t *testing.T) {
	proj := NewUTMProjector(-122.45, 37.55)
	lattice := CityLattice(proj, [4]float64{37.5, -122.5, 37.6, -122.4}, 500)
	if len(lattice) == 0 {
		t.Fatal("empty lattice")
	}
	for name, gjson := range map[string]string{"none stored": "", "unparseable": `{"type":"Polygon","coordinates":`} {
		got, err := ClipHexesToBoundaryGeoJSON(context.Background(), lattice, gjson, proj, nil)
		if !errors.Is(err, ErrNoClipBoundary) {
			t.Errorf("%s: err = %v, want ErrNoClipBoundary", name, err)
		}
		if len(got) != len(lattice) {
			t.Errorf("%s: %d hexes returned, want the whole lattice of %d", name, len(got), len(lattice))
		}
	}

	inner := `{"type":"Polygon","coordinates":[[[-122.47,37.53],[-122.43,37.53],[-122.43,37.57],[-122.47,37.57],[-122.47,37.53]]]}`
	got, err := ClipHexesToBoundaryGeoJSON(context.Background(), lattice, inner, proj, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || len(got) >= len(lattice) {
		t.Errorf("clipped to an inner boundary: %d of %d hexes kept", len(got), len(lattice))
	}
}

// A hex clipped so that its lattice centre falls outside what remains is
// measured from a point on the remaining part; a hex that still holds its
// centre (and an unclipped one) is measured from the centre.
func TestHex_InteriorXY(t *testing.T) {
	hexes := HexGrid(0, 0, 500, 500, 100)
	h := hexes[len(hexes)/2]
	if x, y := h.InteriorXY(); x != h.CenterX || y != h.CenterY {
		t.Errorf("unclipped hex: InteriorXY = (%v, %v), want its centre (%v, %v)", x, y, h.CenterX, h.CenterY)
	}
	// Keep only a 40 m strip along the hex's north edge: the centre is gone.
	strip := makeRect(h.CenterX-40, h.CenterY+45, h.CenterX+40, h.CenterY+85)
	clipped := ClipHexesToBoundary(context.Background(), []Hex{h}, strip, nil)
	if len(clipped) != 1 {
		t.Fatalf("clip = %d hexes, want 1", len(clipped))
	}
	c := clipped[0]
	x, y := c.InteriorXY()
	pt := geom.NewPoint(geom.Coordinates{XY: geom.XY{X: x, Y: y}}).AsGeometry()
	if !geom.Intersects(pt, c.Geom) || y < h.CenterY+45 {
		t.Errorf("clipped hex: InteriorXY = (%v, %v), want a point on the strip (y >= %v)", x, y, h.CenterY+45)
	}
	// A clip that keeps the centre leaves it in use.
	half := makeRect(h.CenterX-200, h.CenterY-200, h.CenterX+200, h.CenterY+10)
	kept := ClipHexesToBoundary(context.Background(), []Hex{h}, half, nil)
	if x, y := kept[0].InteriorXY(); x != h.CenterX || y != h.CenterY {
		t.Errorf("clip keeping the centre: InteriorXY = (%v, %v), want the centre", x, y)
	}
}
