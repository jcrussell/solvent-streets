package ingest

import (
	"math"

	"github.com/jcrussell/solvent-streets/internal/geo"
)

// LandProbes are the lon/lat points the water strip treats as city land: a
// candidate water polygon that holds more than MaxHits of them is land-side
// and is dropped (see FetchOSMWater).
//
// Sparse probes (Dense == false) are a handful of interior points of the
// boundary, one per sub-polygon (InteriorLandProbes), and MaxHits is 0: a
// single hit rejects a candidate. Dense probes are thousands of points taken
// from the city's roads, and MaxHits allows the few that legitimately sit in
// water.
type LandProbes struct {
	Points  [][2]float64
	MaxHits int
	Dense   bool
}

// InteriorLandProbes returns one probe per sub-polygon of boundaryGJSON
// (geo.InteriorPoints): sparse, with no hit allowance.
func InteriorLandProbes(boundaryGJSON string) (LandProbes, error) {
	pts, err := geo.InteriorPoints(boundaryGJSON)
	if err != nil {
		return LandProbes{}, err
	}
	return LandProbes{Points: pts}, nil
}

// landProbeIndex answers "how many probes lie inside this ring" for the
// many candidate rings of one water fetch. Probes are bucketed into a
// uniform grid over their bbox, so a ring visits only the cells its own
// bbox overlaps; with many probes in a ring's bbox the ring is edge-indexed
// (indexedRing), so each point-in-ring test reads one band of edges rather
// than every vertex. A ring with few probes in its bbox is tested with
// pointInRing directly. Both paths compute the same ray-cast parity, so the
// count does not depend on which one ran. Coastal cities produce hundreds
// of water polygons, and dense road probes number in the hundreds of
// thousands (solvent-streets-m3kj).
type landProbeIndex struct {
	probes [][2]float64
	// Overall bbox of every probe (lon/lat). Inclusive; degenerate when
	// len(probes)==1.
	minX, minY, maxX, maxY float64

	// Grid buckets: cells[row*cols+col] holds indexes into probes.
	cols, rows   int
	cellW, cellH float64
	cells        [][]int32
}

// probeGridTarget is the mean number of probes per grid cell the index aims
// for. Small enough that a ring's bbox reads few probes outside it, large
// enough that the grid stays a small fraction of the probe slice.
const probeGridTarget = 4

// indexedRingMinProbes is the number of probes inside a ring's bbox from
// which the ring is edge-indexed before testing. Below it, building the
// index costs more than the plain O(V) ray casts it saves.
const indexedRingMinProbes = 16

func newLandProbeIndex(probes [][2]float64) *landProbeIndex {
	idx := &landProbeIndex{probes: probes}
	if len(probes) == 0 {
		return idx
	}
	idx.minX, idx.maxX = probes[0][0], probes[0][0]
	idx.minY, idx.maxY = probes[0][1], probes[0][1]
	for _, p := range probes[1:] {
		idx.minX = math.Min(idx.minX, p[0])
		idx.maxX = math.Max(idx.maxX, p[0])
		idx.minY = math.Min(idx.minY, p[1])
		idx.maxY = math.Max(idx.maxY, p[1])
	}
	side := max(1, int(math.Sqrt(float64(len(probes))/probeGridTarget)))
	idx.cols, idx.rows = side, side
	idx.cellW = (idx.maxX - idx.minX) / float64(side)
	idx.cellH = (idx.maxY - idx.minY) / float64(side)
	idx.cells = make([][]int32, side*side)
	for i, p := range probes {
		c := idx.cellOf(p[0], p[1])
		idx.cells[c] = append(idx.cells[c], int32(i))
	}
	return idx
}

// col, row and cellOf map a coordinate to its grid cell, clamped to the
// grid so a coordinate outside the probe bbox maps to the nearest edge cell.
func (l *landProbeIndex) col(x float64) int { return gridStep(x, l.minX, l.cellW, l.cols) }
func (l *landProbeIndex) row(y float64) int { return gridStep(y, l.minY, l.cellH, l.rows) }
func (l *landProbeIndex) cellOf(x, y float64) int {
	return l.row(y)*l.cols + l.col(x)
}

func gridStep(v, origin, size float64, n int) int {
	if size <= 0 {
		return 0
	}
	return min(n-1, max(0, int((v-origin)/size)))
}

func (l *landProbeIndex) Len() int { return len(l.probes) }

// inBBox returns the probes inside the inclusive bbox, read from the grid
// cells it overlaps.
func (l *landProbeIndex) inBBox(minX, minY, maxX, maxY float64) [][2]float64 {
	var out [][2]float64
	for r := l.row(minY); r <= l.row(maxY); r++ {
		for c := l.col(minX); c <= l.col(maxX); c++ {
			for _, i := range l.cells[r*l.cols+c] {
				p := l.probes[i]
				if p[0] < minX || p[0] > maxX || p[1] < minY || p[1] > maxY {
					continue
				}
				out = append(out, p)
			}
		}
	}
	return out
}

// candidates returns the probes inside ring's bbox, or nil when the ring
// cannot contain any.
func (l *landProbeIndex) candidates(ring [][2]float64) [][2]float64 {
	if len(ring) == 0 || len(l.probes) == 0 {
		return nil
	}
	rMinX, rMinY, rMaxX, rMaxY := ringBBox(ring)
	if rMaxX < l.minX || rMinX > l.maxX || rMaxY < l.minY || rMinY > l.maxY {
		return nil
	}
	return l.inBBox(rMinX, rMinY, rMaxX, rMaxY)
}

// RingHits returns the number of probes inside ring. Used by the
// coastline closure rule to detect candidate rings that overlap city
// land (those rings are land-side and would over-subtract).
func (l *landProbeIndex) RingHits(ring [][2]float64) int {
	pts := l.candidates(ring)
	if len(pts) == 0 {
		return 0
	}
	in := ringTester(ring, len(pts))
	hits := 0
	for _, p := range pts {
		if in(p) {
			hits++
		}
	}
	return hits
}

// PolygonHits returns the number of probes that lie set-theoretic-
// inside the polygon (inside the outer ring AND outside every hole).
// A probe that lands in a hole is geometrically NOT in the water
// polygon — the hole represents a non-water region (typically a real
// island within a lake), so the polygon doesn't claim that area.
func (l *landProbeIndex) PolygonHits(p waterPolygon) int {
	pts := l.candidates(p.outer)
	if len(pts) == 0 {
		return 0
	}
	inOuter := ringTester(p.outer, len(pts))
	var inside [][2]float64
	for _, probe := range pts {
		if inOuter(probe) {
			inside = append(inside, probe)
		}
	}
	for _, hole := range p.holes {
		if len(inside) == 0 {
			break
		}
		inHole := ringTester(hole, len(inside))
		kept := inside[:0]
		for _, probe := range inside {
			if !inHole(probe) {
				kept = append(kept, probe)
			}
		}
		inside = kept
	}
	return len(inside)
}

// ringTester returns a point-in-ring predicate for ring that will be asked
// about n points: pointInRing itself for a few, an indexedRing for many.
func ringTester(ring [][2]float64, n int) func([2]float64) bool {
	if n < indexedRingMinProbes || len(ring) < 4 {
		return func(p [2]float64) bool { return pointInRing(p, ring) }
	}
	return newIndexedRing(ring).contains
}

// indexedRing is a ring with its edges bucketed into horizontal bands, so
// a ray cast reads only the edges that span the point's latitude band.
// One band per four vertices keeps a band to a handful of edges on a
// coastline-like ring (short edges), at an O(V) build cost: on
// BenchmarkRingHits_150kRoads_10kRing that is about 3x faster than sqrt(V)
// bands.
type indexedRing struct {
	ring       [][2]float64
	minY, step float64
	bands      [][]int32 // edge i joins ring[i] and ring[i-1] (ring[0] and ring[len-1] for i == 0)
}

func newIndexedRing(ring [][2]float64) *indexedRing {
	n := len(ring)
	minY, maxY := ring[0][1], ring[0][1]
	for _, p := range ring[1:] {
		minY = math.Min(minY, p[1])
		maxY = math.Max(maxY, p[1])
	}
	nb := max(1, n/4)
	r := &indexedRing{ring: ring, minY: minY, step: (maxY - minY) / float64(nb), bands: make([][]int32, nb)}
	for i := range n {
		j := i - 1
		if i == 0 {
			j = n - 1
		}
		lo, hi := ring[i][1], ring[j][1]
		if lo > hi {
			lo, hi = hi, lo
		}
		if lo == hi {
			continue // a horizontal edge never satisfies the crossing test
		}
		for b := r.band(lo); b <= r.band(hi); b++ {
			r.bands[b] = append(r.bands[b], int32(i))
		}
	}
	return r
}

func (r *indexedRing) band(y float64) int { return gridStep(y, r.minY, r.step, len(r.bands)) }

// contains is pointInRing over only the edges in p's band. An edge outside
// the band cannot straddle p's latitude, and the crossing test evaluates
// the identical expression on the identical (i, j) vertex pair, so the
// parity is pointInRing's exactly.
func (r *indexedRing) contains(p [2]float64) bool {
	x, y := p[0], p[1]
	inside := false
	n := len(r.ring)
	for _, e := range r.bands[r.band(y)] {
		i := int(e)
		j := i - 1
		if i == 0 {
			j = n - 1
		}
		xi, yi := r.ring[i][0], r.ring[i][1]
		xj, yj := r.ring[j][0], r.ring[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			inside = !inside
		}
	}
	return inside
}
