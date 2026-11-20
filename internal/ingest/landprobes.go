package ingest

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/jcrussell/solvent-streets/internal/db"
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
	// Boundary is the unstripped city boundary (GeoJSON) dense probes were
	// taken in. It decides nothing; the coastline faces use it to warn only
	// about faces that matter to the city (coastlineFaces).
	Boundary string
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

// RoadLandProbes returns dense land probes taken from roads: one point per
// road that is not built over or through water, kept only when it lies
// inside boundaryGJSON (the unstripped boundary), at most one per
// roadProbeCellDeg grid cell. The point is the road's middle vertex (the
// midpoint of a two-vertex road), not its first: a bridge approach starts on
// the shore, and a road's middle is the part least likely to be at a
// shoreline. MaxHits is waterLandHitFloor, or waterLandHitFrac of the
// probes when that is more. It returns no points when fewer than
// RoadProbeMinCells cells qualify, or when boundaryGJSON is not a polygon;
// the caller then falls back to InteriorLandProbes.
func RoadLandProbes(roads []db.Feature, boundaryGJSON string) LandProbes {
	return roadLandProbes(roads, boundaryGJSON, RoadProbeMinCells)
}

// RoadProbeMinCells is the fewest road probes (grid cells) that make dense
// probes. Dense probes reject a water candidate only above the allowance,
// at least waterLandHitFloor hits, and lift the bbox-area cap; a handful of
// probes could hardly ever exceed that floor, so they would reject almost
// nothing, which is weaker than the one-hit rule of the interior points.
// Ten times the floor keeps a land-side candidate of a sparsely-roaded city
// (or one whose road fetch came back nearly empty) clearly over it.
const RoadProbeMinCells = 10 * waterLandHitFloor

func roadLandProbes(roads []db.Feature, boundaryGJSON string, minCells int) LandProbes {
	polys, err := parsePolygons(boundaryGJSON)
	if err != nil || len(roads) == 0 {
		return LandProbes{}
	}
	inBoundary := newPolygonsTester(polys)
	seen := make(map[[2]int64]bool)
	var pts [][2]float64
	for _, r := range roads {
		if overWater(r.Tags) {
			continue
		}
		p, ok := middlePoint(r.GeometryJSON)
		if !ok {
			continue
		}
		cell := [2]int64{int64(math.Floor(p[0] / roadProbeCellDeg)), int64(math.Floor(p[1] / roadProbeCellDeg))}
		if seen[cell] || !inBoundary(p) {
			continue
		}
		seen[cell] = true
		pts = append(pts, p)
	}
	if len(pts) == 0 || len(pts) < minCells {
		return LandProbes{}
	}
	return LandProbes{Points: pts, MaxHits: waterLandHitAllowance(len(pts)), Dense: true, Boundary: boundaryGJSON}
}

// waterLandHitAllowance is the MaxHits of n dense probes.
func waterLandHitAllowance(n int) int {
	return max(waterLandHitFloor, int(math.Ceil(waterLandHitFrac*float64(n))))
}

// overWater reports a road whose tags put it on a structure over or through
// water (a bridge, tunnel, pier, breakwater, groyne, ford, embankment,
// causeway or ferry route), so its points are not evidence of land. ArcGIS
// centerlines carry no such tags; the hit allowance absorbs their bridges.
func overWater(tags map[string]string) bool {
	for _, k := range []string{"bridge", "tunnel"} {
		if v, ok := tags[k]; ok && v != "no" {
			return true
		}
	}
	switch tags["man_made"] {
	case "pier", "breakwater", "groyne":
		return true
	}
	if _, ok := tags["causeway"]; ok {
		return true
	}
	return tags["ford"] == osmYes || tags["embankment"] == osmYes || tags["route"] == "ferry"
}

// osmYes is the OSM tag value "yes".
const osmYes = "yes"

// middlePoint returns the middle vertex of a road's first line (the
// midpoint of its only segment when it has two vertices).
func middlePoint(geometryJSON string) ([2]float64, bool) {
	lines := lineParts(geometryJSON)
	if len(lines) == 0 || len(lines[0]) == 0 {
		return [2]float64{}, false
	}
	l := lines[0]
	if len(l) == 2 {
		return [2]float64{(l[0][0] + l[1][0]) / 2, (l[0][1] + l[1][1]) / 2}, true
	}
	return l[len(l)/2], true
}

// lineParts returns the vertex sequences of a GeoJSON LineString,
// MultiLineString, Polygon (its rings) or Point (one one-vertex part).
// Other types, and unparseable geometry, return nil.
func lineParts(geometryJSON string) [][][2]float64 {
	var g struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if json.Unmarshal([]byte(geometryJSON), &g) != nil {
		return nil
	}
	switch g.Type {
	case "Point":
		var c [2]float64
		if json.Unmarshal(g.Coordinates, &c) != nil {
			return nil
		}
		return [][][2]float64{{c}}
	case "LineString":
		var c [][2]float64
		if json.Unmarshal(g.Coordinates, &c) != nil {
			return nil
		}
		return [][][2]float64{c}
	case "MultiLineString", "Polygon":
		var c [][][2]float64
		if json.Unmarshal(g.Coordinates, &c) != nil {
			return nil
		}
		return c
	}
	return nil
}

// FeaturesTouchingBBox returns the features with any segment (or, for a
// single point, the point) inside or crossing bbox ([south, west, north,
// east]): the set an Overpass way(bbox) or ArcGIS envelope query over bbox
// returns. A road fetched over a larger bbox and filtered with it is
// therefore the row a fetch over bbox itself would have stored.
func FeaturesTouchingBBox(features []db.Feature, bbox [4]float64) []db.Feature {
	var out []db.Feature
	for _, f := range features {
		if touchesBBox(lineParts(f.GeometryJSON), bbox) {
			out = append(out, f)
		}
	}
	return out
}

func touchesBBox(parts [][][2]float64, bbox [4]float64) bool {
	for _, part := range parts {
		if len(part) == 1 {
			if _, _, ok := liangBarsky(part[0], part[0], bbox); ok {
				return true
			}
		}
		for i := 1; i < len(part); i++ {
			if _, _, ok := liangBarsky(part[i-1], part[i], bbox); ok {
				return true
			}
		}
	}
	return false
}

// parsePolygons reads a GeoJSON Polygon or MultiPolygon as polygons of
// rings (outer first, then holes).
func parsePolygons(gjson string) ([][][][2]float64, error) {
	var g struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(gjson), &g); err != nil {
		return nil, err
	}
	switch g.Type {
	case "Polygon":
		var rings [][][2]float64
		if err := json.Unmarshal(g.Coordinates, &rings); err != nil {
			return nil, err
		}
		return [][][][2]float64{rings}, nil
	case "MultiPolygon":
		var polys [][][][2]float64
		if err := json.Unmarshal(g.Coordinates, &polys); err != nil {
			return nil, err
		}
		return polys, nil
	}
	return nil, fmt.Errorf("not a polygon: %s", g.Type)
}

// polygonTester is one polygon of a boundary with its rings edge-indexed.
type polygonTester struct {
	minX, minY, maxX, maxY float64
	outer                  func([2]float64) bool
	holes                  []func([2]float64) bool
}

func (t polygonTester) contains(p [2]float64) bool {
	if p[0] < t.minX || p[0] > t.maxX || p[1] < t.minY || p[1] > t.maxY || !t.outer(p) {
		return false
	}
	for _, hole := range t.holes {
		if hole(p) {
			return false
		}
	}
	return true
}

// newPolygonsTester returns a point-in-polygons predicate (inside some
// polygon's outer ring and none of its holes), each ring edge-indexed, for
// testing many points against a city boundary.
func newPolygonsTester(polys [][][][2]float64) func([2]float64) bool {
	var ts []polygonTester
	for _, rings := range polys {
		if len(rings) == 0 || len(rings[0]) < 4 {
			continue
		}
		t := polygonTester{outer: ringTester(rings[0], indexedRingMinProbes)}
		t.minX, t.minY, t.maxX, t.maxY = ringBBox(rings[0])
		for _, h := range rings[1:] {
			t.holes = append(t.holes, ringTester(h, indexedRingMinProbes))
		}
		ts = append(ts, t)
	}
	return func(p [2]float64) bool {
		for _, t := range ts {
			if t.contains(p) {
				return true
			}
		}
		return false
	}
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
// thousands.
type landProbeIndex struct {
	probes [][2]float64
	// maxHits is LandProbes.MaxHits: a candidate holding more probes than
	// this holds city land (landHit).
	maxHits int
	// dense is LandProbes.Dense.
	dense bool
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

// newLandProbeIndexFor indexes lp's points with its allowance.
func newLandProbeIndexFor(lp LandProbes) *landProbeIndex {
	idx := newLandProbeIndex(lp.Points)
	idx.maxHits, idx.dense = lp.MaxHits, lp.Dense
	return idx
}

// landHit reports whether a candidate holding hits probes holds city land:
// more hits than the allowance (any hit, for sparse probes).
func (l *landProbeIndex) landHit(hits int) bool { return hits > l.maxHits }

// capArea is the bbox area acceptWaterPolygon caps a water way or relation
// outer at (maxOuterBboxAreaFraction): bboxArea for sparse probes, and 0,
// no cap, for dense ones, whose hit count is the land test instead.
func (l *landProbeIndex) capArea(bboxArea float64) float64 {
	if l.dense {
		return 0
	}
	return bboxArea
}

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
