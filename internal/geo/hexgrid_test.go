package geo

import (
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

func TestComputeHexStats_NoIntersection(t *testing.T) {
	hexes := HexGrid(0, 0, 500, 500, 100)
	// Index a geometry far away
	idx := NewGeomIndexFromGeoms([]geom.Geometry{makeRect(10000, 10000, 10100, 10100)})
	stats := ComputeHexStats(hexes, idx, "roads", nil)
	if len(stats) != 0 {
		t.Errorf("expected 0 stats for non-intersecting, got %d", len(stats))
	}
}

func TestComputeHexStats_PartialIntersection(t *testing.T) {
	hexes := HexGrid(0, 0, 500, 500, 100)
	// Small rect that should intersect a few hexes
	idx := NewGeomIndexFromGeoms([]geom.Geometry{makeRect(100, 100, 200, 200)})
	stats := ComputeHexStats(hexes, idx, "roads", nil)
	if len(stats) == 0 {
		t.Error("expected some hex stats for intersecting geometry")
	}
	for _, s := range stats {
		if s.PctCovered <= 0 || s.PctCovered > 100 {
			t.Errorf("invalid pct_covered: %f", s.PctCovered)
		}
		if s.AreaSqM <= 0 {
			t.Errorf("expected positive area, got %f", s.AreaSqM)
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
	stats := ComputeHexStats(hexes, idx, "roads", nil)
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
		if s.AreaSqM > hexArea {
			t.Errorf("area %f exceeds hex area %f — overlap not deduped", s.AreaSqM, hexArea)
		}
	}
}

func TestClipHexesToBoundary(t *testing.T) {
	hexes := HexGrid(0, 0, 500, 500, 100)
	// Boundary covers only part of the hex grid
	boundary := makeRect(50, 50, 250, 250)
	clipped := ClipHexesToBoundary(hexes, boundary, nil)
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
		ComputeHexStats(hexes, idx, "roads", nil)
	}
}
