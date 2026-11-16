package ingest

import (
	"context"
	"math"

	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/logs"
)

// waterClipPadM is how far past the query bbox the water polygons are
// clipped (clipWaterToBBox). The city boundary lies inside the bbox, so the
// clip's edges, which can run back and forth along the clip line where a
// concave polygon leaves and re-enters it, stay clear of the boundary.
const waterClipPadM = 100

// clipWaterToBBox clips every water polygon (outer and holes) to bbox
// padded by waterClipPadM. Only the part of the water inside the city
// boundary is ever used, and the boundary lies inside bbox, so subtracting
// the clipped water gives the same boundary; but a lake or river relation
// far larger than the city (Lake Michigan beside Milwaukee) otherwise
// reaches geo.SubtractGeoJSON with every vertex, and its projection and
// cleanup dominate the run. A polygon already inside the padded bbox is
// returned unchanged; one with no water left inside the padded bbox
// (waterless) is dropped.
func clipWaterToBBox(ctx context.Context, polys []waterPolygon, bbox [4]float64) []waterPolygon {
	clip := geo.PadBBox(bbox, waterClipPadM)
	out := make([]waterPolygon, 0, len(polys))
	for _, p := range polys {
		outer, ok := clipRingToBBox(p.outer, clip)
		if !ok {
			continue
		}
		cp := waterPolygon{outer: outer}
		for _, h := range p.holes {
			// A hole wholly outside the padded bbox (one of Lake
			// Champlain's big islands, miles from Burlington) can change
			// nothing inside it, and kept unclipped its area would count
			// against the clipped outer's in waterless.
			if disjointFromBBox(h, clip) {
				continue
			}
			if hole, ok := clipRingToBBox(h, clip); ok {
				cp.holes = append(cp.holes, hole)
			}
		}
		if waterless(cp) {
			logs.From(ctx).Debug("water: dropped a polygon with no water left inside the bbox",
				"outer_vertices", len(p.outer), "holes", len(p.holes))
			continue
		}
		out = append(out, cp)
	}
	return out
}

// waterless reports a clipped polygon with no water left: its outer has no
// area, or its holes cover it, as when the city lies wholly on an island
// (an inner ring) of a lake that also encloses the bbox, so that the outer
// and the island both clip to the padded bbox itself. Holes of a valid
// polygon do not overlap, so their areas add up. Left in, it would reach
// the subtract as an empty polygon and fail it.
func waterless(p waterPolygon) bool {
	outer := math.Abs(ringSignedArea(p.outer))
	var holes float64
	for _, h := range p.holes {
		holes += math.Abs(ringSignedArea(h))
	}
	return outer == 0 || holes >= outer*(1-1e-9)
}

// clipRingToBBox clips a closed ring to bbox ([south, west, north, east])
// with Sutherland-Hodgman, one half-plane per bbox edge. A ring inside bbox,
// or wholly outside it, is returned as is (the same slice). ok is false
// when a ring crossing the bbox edge leaves nothing inside. Where a concave ring leaves bbox and comes back, the result runs
// along the bbox edge and back, a zero-width spur that the subtract's
// polygon cleanup removes; clipWaterToBBox keeps such spurs off the city
// boundary by padding the bbox.
func clipRingToBBox(ring [][2]float64, bbox [4]float64) ([][2]float64, bool) {
	if len(ring) < 4 {
		return nil, false
	}
	south, west, north, east := bbox[0], bbox[1], bbox[2], bbox[3]
	minX, minY, maxX, maxY := ringBBox(ring)
	inside := minX >= west && maxX <= east && minY >= south && maxY <= north
	disjoint := maxX < west || minX > east || maxY < south || minY > north
	if inside || disjoint {
		// A ring wholly outside bbox cannot touch the boundary; it is kept
		// as is rather than dropped, which Overpass, answering only water
		// that touches the bbox, never sends anyway.
		return ring, true
	}
	pts := ring[:len(ring)-1] // open: the closing vertex repeats the first
	for _, e := range [4]struct {
		in    func(p [2]float64) bool
		cross func(a, b [2]float64) [2]float64
	}{
		{func(p [2]float64) bool { return p[0] >= west }, func(a, b [2]float64) [2]float64 { return atX(a, b, west) }},
		{func(p [2]float64) bool { return p[0] <= east }, func(a, b [2]float64) [2]float64 { return atX(a, b, east) }},
		{func(p [2]float64) bool { return p[1] >= south }, func(a, b [2]float64) [2]float64 { return atY(a, b, south) }},
		{func(p [2]float64) bool { return p[1] <= north }, func(a, b [2]float64) [2]float64 { return atY(a, b, north) }},
	} {
		if len(pts) == 0 {
			return nil, false
		}
		next := make([][2]float64, 0, len(pts))
		prev := pts[len(pts)-1]
		for _, cur := range pts {
			switch curIn, prevIn := e.in(cur), e.in(prev); {
			case curIn && prevIn:
				next = append(next, cur)
			case curIn:
				next = append(next, e.cross(prev, cur), cur)
			case prevIn:
				next = append(next, e.cross(prev, cur))
			}
			prev = cur
		}
		pts = next
	}
	if len(pts) < 3 {
		return nil, false
	}
	return append(pts, pts[0]), true
}

// atX and atY are where segment a→b crosses the line x = v (y = v).
func atX(a, b [2]float64, v float64) [2]float64 {
	t := (v - a[0]) / (b[0] - a[0])
	return [2]float64{v, a[1] + t*(b[1]-a[1])}
}

func atY(a, b [2]float64, v float64) [2]float64 {
	t := (v - a[1]) / (b[1] - a[1])
	return [2]float64{a[0] + t*(b[0]-a[0]), v}
}

// disjointFromBBox reports a ring whose envelope misses bbox.
func disjointFromBBox(ring [][2]float64, bbox [4]float64) bool {
	if len(ring) == 0 {
		return true
	}
	minX, minY, maxX, maxY := ringBBox(ring)
	return maxX < bbox[1] || minX > bbox[3] || maxY < bbox[0] || minY > bbox[2]
}
