package geo

import (
	"math"
	"testing"
)

func lonLatArea(polys []LonLatPolygon) float64 {
	var a float64
	for _, p := range polys {
		a += math.Abs(SignedRingArea(p[0]))
		for _, h := range p[1:] {
			a -= math.Abs(SignedRingArea(h))
		}
	}
	return a
}

func TestIntersectLonLat(t *testing.T) {
	square := func(x0, y0, x1, y1 float64) LonLatPolygon {
		return LonLatPolygon{{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}, {x0, y0}}}
	}
	// A unit square with a hole, cut by a half-plane-like square.
	holed := LonLatPolygon{
		{{0, 0}, {1, 0}, {1, 1}, {0, 1}, {0, 0}},
		{{0.4, 0.4}, {0.6, 0.4}, {0.6, 0.6}, {0.4, 0.6}, {0.4, 0.4}},
	}
	got, err := IntersectLonLat([]LonLatPolygon{holed}, []LonLatPolygon{square(0.5, -1, 2, 2)})
	if err != nil {
		t.Fatal(err)
	}
	if a := lonLatArea(got); math.Abs(a-(0.5-0.02)) > 1e-12 {
		t.Errorf("area = %v, want 0.48", a)
	}
	// Disjoint operands, and an empty one, give nothing.
	if got, err := IntersectLonLat([]LonLatPolygon{square(0, 0, 1, 1)}, []LonLatPolygon{square(2, 2, 3, 3)}); err != nil || len(got) != 0 {
		t.Errorf("disjoint = %v, %v; want none", got, err)
	}
	if got, err := IntersectLonLat(nil, []LonLatPolygon{square(0, 0, 1, 1)}); err != nil || len(got) != 0 {
		t.Errorf("empty operand = %v, %v; want none", got, err)
	}
	// A self-touching (bow-tie) ring is cleaned rather than failing.
	bowtie := LonLatPolygon{{{0, 0}, {1, 1}, {1, 0}, {0, 1}, {0, 0}}}
	if _, err := IntersectLonLat([]LonLatPolygon{bowtie}, []LonLatPolygon{square(0, 0, 1, 1)}); err != nil {
		t.Errorf("bow-tie: %v", err)
	}
}
