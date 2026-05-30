package ingest

// stitchInput pairs an OSM way's ref id with its coordinate sequence.
// stitchRings consumes these so it can report the ids of member ways
// whose partial chains had to be dropped.
type stitchInput struct {
	id     int64
	coords [][2]float64
}

// stitchRings chains open ways into closed rings by matching endpoints
// exactly, extending only at the ring's tail. Ways that cannot be closed
// are dropped and their ids returned in dropped. Each input way is consumed
// at most once. Time complexity is O(n²) in the number of ways, which is
// fine for the dozens-of-segments-per-relation scale of OSM water polygons.
// Water/coastline stitching uses this exact, tail-only variant; admin
// boundaries use stitchRingsBidi.
func stitchRings(ways []stitchInput) (rings [][][2]float64, dropped []int64) {
	used := make([]bool, len(ways))

	for i := range ways {
		if used[i] || len(ways[i].coords) < 2 {
			continue
		}
		used[i] = true
		ring := append([][2]float64{}, ways[i].coords...)
		consumed := []int{i}

		for ring[0] != ring[len(ring)-1] {
			extended, next, nextIdx := extendRing(ring, ways, used)
			if !extended {
				break
			}
			ring = next
			consumed = append(consumed, nextIdx)
		}

		if isClosedRing(ring) {
			rings = append(rings, ring)
		} else {
			for _, c := range consumed {
				dropped = append(dropped, ways[c].id)
			}
		}
	}
	return rings, dropped
}

// stitchRingsBidi chains open ways into closed rings, matching each candidate
// way against BOTH ends of the growing chain (prepending or appending, with
// reversal as needed). OSM relations whose member ways are not consistently
// oriented — e.g. Denver's admin boundary, relation 1411339 — dead-end the
// tail-only stitchRings and lose the whole ring; a correct ring walk must be
// able to grow in either direction.
//
// It additionally snaps a fully-assembled but not-quite-closed chain shut when
// its two ends lie within boundaryCloseTolDeg (Euclidean lon/lat degrees): OSM
// admin boundaries frequently have a sub-~100 m gap where two adjacent member
// ways fail to share an exact node. Tolerance only ever closes a chain against
// its OWN endpoints, so distinct rings stay distinct and a chain with a large
// gap still drops.
func stitchRingsBidi(ways []stitchInput) (rings [][][2]float64, dropped []int64) {
	used := make([]bool, len(ways))

	for i := range ways {
		if used[i] || len(ways[i].coords) < 2 {
			continue
		}
		used[i] = true
		ring := append([][2]float64{}, ways[i].coords...)
		consumed := []int{i}

		for ring[0] != ring[len(ring)-1] {
			grew, next, idx := growRing(ring, ways, used)
			if !grew {
				break
			}
			ring = next
			consumed = append(consumed, idx)
		}

		if !isClosedRing(ring) && len(ring) >= 4 &&
			sqDistDeg(ring[0], ring[len(ring)-1]) <= boundaryCloseTolDeg*boundaryCloseTolDeg {
			ring = append(ring, ring[0])
		}

		if isClosedRing(ring) {
			rings = append(rings, ring)
		} else {
			for _, c := range consumed {
				dropped = append(dropped, ways[c].id)
			}
		}
	}
	return rings, dropped
}

// growRing splices the first unused way that connects to EITHER end of ring
// onto that end (reversing the way when its orientation requires it), marks it
// used, and returns the extended ring. Returns (false, ring, -1) when no way
// connects to either end.
func growRing(ring [][2]float64, ways []stitchInput, used []bool) (bool, [][2]float64, int) {
	head, tail := ring[0], ring[len(ring)-1]
	for j := range ways {
		if used[j] || len(ways[j].coords) < 2 {
			continue
		}
		w := ways[j].coords
		last := len(w) - 1
		switch {
		case tail == w[0]: // append forward, dropping the shared node
			ring = append(ring, w[1:]...)
		case tail == w[last]: // append reversed
			for k := last - 1; k >= 0; k-- {
				ring = append(ring, w[k])
			}
		case head == w[last]: // prepend forward, dropping the shared node
			ring = append(append([][2]float64{}, w[:last]...), ring...)
		case head == w[0]: // prepend reversed
			rev := make([][2]float64, 0, last)
			for k := last; k >= 1; k-- {
				rev = append(rev, w[k])
			}
			ring = append(rev, ring...)
		default:
			continue
		}
		used[j] = true
		return true, ring, j
	}
	return false, ring, -1
}

// sqDistDeg is the squared Euclidean distance between two lon/lat points in
// degrees. Used only to test a small closure tolerance, where the planar
// approximation is more than adequate.
func sqDistDeg(a, b [2]float64) float64 {
	dx := a[0] - b[0]
	dy := a[1] - b[1]
	return dx*dx + dy*dy
}

// extendRing finds an unused way whose endpoint matches ring's tail and
// appends it to ring (reversing the way if needed). The matched way is
// marked used. Returns (false, ring, -1) when no way matches.
func extendRing(ring [][2]float64, ways []stitchInput, used []bool) (bool, [][2]float64, int) {
	tail := ring[len(ring)-1]
	for j := range ways {
		if used[j] || len(ways[j].coords) < 2 {
			continue
		}
		w := ways[j].coords
		switch {
		case tail == w[0]:
			ring = append(ring, w[1:]...)
		case tail == w[len(w)-1]:
			for k := len(w) - 2; k >= 0; k-- {
				ring = append(ring, w[k])
			}
		default:
			continue
		}
		used[j] = true
		return true, ring, j
	}
	return false, ring, -1
}

func isClosedRing(coords [][2]float64) bool {
	return len(coords) >= 4 && coords[0] == coords[len(coords)-1]
}

// pointInRing returns true if p is strictly inside ring using ray casting.
// The ring is in lon/lat order; that's fine because point-in-polygon is
// topological — it does not require an equal-area projection.
func pointInRing(p [2]float64, ring [][2]float64) bool {
	if len(ring) < 4 {
		return false
	}
	x, y := p[0], p[1]
	inside := false
	for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
		xi, yi := ring[i][0], ring[i][1]
		xj, yj := ring[j][0], ring[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			inside = !inside
		}
	}
	return inside
}

// ringSignedArea returns twice the signed area of ring in lon/lat
// coordinates (x=lon east-positive, y=lat north-positive). Only the
// sign matters for orientation, so the divide-by-two is skipped.
// Positive = CCW, negative = CW. A degenerate ring (fewer than 4
// vertices) returns 0.
func ringSignedArea(ring [][2]float64) float64 {
	if len(ring) < 4 {
		return 0
	}
	var sum float64
	for i := range len(ring) - 1 {
		sum += ring[i][0]*ring[i+1][1] - ring[i+1][0]*ring[i][1]
	}
	return sum
}

// ringIsCW reports whether ring is clockwise in lon/lat with y=lat
// north-positive. OSM coastline rule places water on the right of
// forward walk, so a closed coastline ring is CW when water lies
// inside (a lake — the case our pipeline treats as a water polygon)
// and CCW when water lies outside (an island, with land inside).
func ringIsCW(ring [][2]float64) bool {
	return ringSignedArea(ring) < 0
}
