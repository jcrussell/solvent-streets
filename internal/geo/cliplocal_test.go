package geo

import (
	"context"
	"math"
	"testing"

	"github.com/peterstace/simplefeatures/geom"
)

// wigglyBoundary is a many-vertex MultiPolygon shaped like a real city
// limit: a ring whose radius wanders (so its edge passes through hundreds of
// hexes, enough for ClipHexesToBoundaryLocal's quadtree to split many
// times), with a hole (interior water) and a detached island.
func wigglyBoundary() geom.Geometry {
	ring := func(cx, cy, r, amp float64, n int, reverse bool) geom.LineString {
		coords := make([]float64, 0, 2*(n+1))
		for i := range n {
			k := i
			if reverse {
				k = n - i
			}
			a := 2 * math.Pi * float64(k) / float64(n)
			rr := r + amp*math.Sin(7*a) + amp/3*math.Sin(31*a)
			coords = append(coords, cx+rr*math.Cos(a), cy+rr*math.Sin(a))
		}
		coords = append(coords, coords[0], coords[1])
		return geom.NewLineString(geom.NewSequence(coords, geom.DimXY))
	}
	main := geom.NewPolygon([]geom.LineString{
		ring(0, 0, 2000, 250, 3000, false),
		ring(-300, 200, 400, 60, 500, true),
	})
	island := geom.NewPolygon([]geom.LineString{ring(2900, 0, 300, 40, 400, false)})
	return geom.NewMultiPolygon([]geom.Polygon{main, island}).AsGeometry()
}

// TestClipHexesToBoundaryLocal_MatchesClipHexesToBoundary pins the localized
// clip to the one compute stores results on: the same hexes, in the same
// order, with the same clipped areas (to rounding), over a boundary with
// concave wiggles, a hole and an island, so every path runs: interior keep,
// exterior drop (outside, in the hole, between the parts), and straddling
// hexes clipped through several quadtree levels.
func TestClipHexesToBoundaryLocal_MatchesClipHexesToBoundary(t *testing.T) {
	boundary := wigglyBoundary()
	hexes := HexGrid(-2500, -2500, 3400, 2500, 60)
	ctx := context.Background()
	want := ClipHexesToBoundary(ctx, hexes, boundary, nil)
	got := ClipHexesToBoundaryLocal(ctx, hexes, boundary)

	if len(got) != len(want) {
		t.Fatalf("local clip kept %d hexes, ClipHexesToBoundary %d", len(got), len(want))
	}
	straddling := 0
	for i := range want {
		if got[i].ID != want[i].ID {
			t.Fatalf("hex %d: id %s, want %s (same hexes in the same order)", i, got[i].ID, want[i].ID)
		}
		g, w := got[i].Geom.Area(), want[i].Geom.Area()
		if math.Abs(g-w) > 1e-6*math.Max(1, w) {
			t.Errorf("hex %s: area %.9f, want %.9f", want[i].ID, g, w)
		}
		if math.Abs(w-hexes[0].Geom.Area()) > 1e-6 {
			straddling++
		}
	}
	// Guard the fixture: the quadtree only splits past localClipLeafHexes
	// straddling hexes, so make sure there are many.
	if straddling < 20*localClipLeafHexes {
		t.Fatalf("fixture clips only %d hexes; want enough to split the quadtree", straddling)
	}
}

// TestClipHexesToBoundaryLocal_Cancelled returns nothing rather than a
// partial grid when the context is already cancelled.
func TestClipHexesToBoundaryLocal_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := ClipHexesToBoundaryLocal(ctx, HexGrid(-2500, -2500, 3400, 2500, 60), wigglyBoundary()); len(got) != 0 {
		t.Errorf("cancelled clip returned %d hexes, want none", len(got))
	}
}
