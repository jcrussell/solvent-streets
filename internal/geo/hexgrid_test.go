package geo

import (
	"math"
	"testing"
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
	// Union geometry far away
	rect := makeRect(10000, 10000, 10100, 10100)
	proj := &UTMProjector{Zone: 10, Northern: true}
	stats := ComputeHexStats(hexes, rect, "pavements", proj)
	if len(stats) != 0 {
		t.Errorf("expected 0 stats for non-intersecting, got %d", len(stats))
	}
}

func TestComputeHexStats_PartialIntersection(t *testing.T) {
	hexes := HexGrid(0, 0, 500, 500, 100)
	// Small rect that should intersect a few hexes
	rect := makeRect(100, 100, 200, 200)
	proj := &UTMProjector{Zone: 10, Northern: true}
	stats := ComputeHexStats(hexes, rect, "pavements", proj)
	if len(stats) == 0 {
		t.Error("expected some hex stats for intersecting geometry")
	}
	for _, s := range stats {
		if s.PctCovered <= 0 || s.PctCovered > 100 {
			t.Errorf("invalid pct_covered: %f", s.PctCovered)
		}
		if s.AreaSqFt <= 0 {
			t.Errorf("expected positive area, got %f", s.AreaSqFt)
		}
	}
}
