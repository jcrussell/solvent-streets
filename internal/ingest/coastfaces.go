package ingest

import (
	"context"
	"fmt"
	"math"

	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/logs"
)

// coastFace is one face of the bbox as cut by the open coastline
// sub-chains (coastlineFaces), as lon/lat polygons.
type coastFace struct {
	polys []geo.LonLatPolygon
}

// Face classification constants (dense probes only), from the batch-1
// replay (solvent-streets-aot4). A face holding at most the allowance
// (LandProbes.MaxHits) is water; one holding more is land. Each landmass
// the coastlines cut off is its own face, bounded across its coastline by
// water faces, so its roads keep it however few they are beside the
// mainland's: Juneau's borough part of Admiralty Island (12 road cells,
// beside a sea holding 7) and an island crossing the bbox edge with 4 cells
// stay land.
//
// The one exception is a water face holding a few more probes than the
// allowance (piers, wharves, untagged causeway roads) next to a land face
// holding thousands across its coastline: Alameda's bay face holds 4
// against the island's 424, Juneau's sea 7 against the mainland's 1422,
// over an allowance of 3. Such a face is water when it holds at most
// faceSliverMaxAllowances times the allowance AND at most
// coastlineLowSideFrac of the most any face across its coastlines holds,
// and is warned about with both counts and its area, since it strips those
// roads. A land face never qualifies: what lies across its coastlines is
// water, holding few probes. New York's waters, once cut into faces, hold
// no more than its allowance (45 of 44,104 probes) and need no exception.
const (
	coastlineLowSideFrac    = 0.05
	faceSliverMaxAllowances = 3
)

// coastlineFaces is the dense-probe coastline assembly
// (solvent-streets-aot4). It cuts the bbox into faces along every open
// coastline sub-chain, each chain splitting the face it lies in into its
// two closures' parts, so every landmass and every stretch of water the
// coastlines bound is its own face. Each face is then classified by the
// road probes it holds, with the islands (closed CCW rings) as holes: the
// water faces are returned. Closed CW rings (lagoons) are decided as
// before (closedCoastlineRing). A chain whose overlay fails is dropped,
// with a warning, and the faces it would have cut stay whole.
//
// boundary is the unstripped city boundary, or nil; it only scopes the
// land-side warning (waterFaces).
func coastlineFaces(ctx context.Context, chains [][][2]float64, bbox [4]float64, probes *landProbeIndex, islands [][][2]float64, boundary []geo.LonLatPolygon) []waterPolygon {
	water, opens := coastlineSubChains(ctx, chains, bbox, probes, islands)
	if len(opens) == 0 && len(islands) == 0 {
		return water
	}
	south, west, north, east := bbox[0], bbox[1], bbox[2], bbox[3]
	faces := []coastFace{{
		polys: []geo.LonLatPolygon{{{{west, south}, {west, north}, {east, north}, {east, south}, {west, south}}}},
	}}
	for _, sub := range opens {
		faces = splitFaces(ctx, faces, sub, bbox)
	}
	return append(water, waterFaces(ctx, faces, opens, probes, islands, boundary)...)
}

// coastlineSubChains clips every chain to bbox: the lagoons (closed CW
// rings) it accepts as water, and the open sub-chains that cut faces.
func coastlineSubChains(ctx context.Context, chains [][][2]float64, bbox [4]float64, probes *landProbeIndex, islands [][][2]float64) (water []waterPolygon, opens [][][2]float64) {
	for _, chain := range chains {
		for _, sub := range clipChainToBBox(startOutside(chain, bbox), bbox) {
			switch {
			case len(sub) < 2:
			case isClosedRing(sub):
				if p, ok := closedCoastlineRing(ctx, sub, probes, islands); ok {
					water = append(water, p)
				}
			default:
				opens = append(opens, sub)
			}
		}
	}
	return water, opens
}

// waterFaces classifies the faces by the probes each holds, with the
// islands as holes, and returns the water ones (see the face
// classification constants). A face's contrast is the most probes any face
// across one of its coastlines holds.
func waterFaces(ctx context.Context, faces []coastFace, opens [][][2]float64, probes *landProbeIndex, islands [][][2]float64, boundary []geo.LonLatPolygon) []waterPolygon {
	parts := make([][]waterPolygon, len(faces))
	hits := make([]int, len(faces))
	for i, f := range faces {
		parts[i] = faceWater(f, islands)
		for _, p := range parts[i] {
			hits[i] += probes.PolygonHits(p)
		}
	}
	contrast := make([]int, len(faces))
	// leftOf and rightOf count the coastlines a face lies left of (land, by
	// OSM's water-on-the-right convention) and right of.
	leftOf := make([]int, len(faces))
	rightOf := make([]int, len(faces))
	for _, sub := range opens {
		l, r := faceAt(faces, sub, 1), faceAt(faces, sub, -1)
		if l < 0 || r < 0 || l == r {
			continue
		}
		contrast[l] = max(contrast[l], hits[r])
		contrast[r] = max(contrast[r], hits[l])
		leftOf[l]++
		rightOf[r]++
	}
	// landByDirection warns about a water face that every coastline bounding
	// it puts on land, when enough of it lies in the city boundary to matter
	// (landSideWarnMinKm2). The decision stands, since coastline direction
	// is sometimes wrong (solvent-streets-yrr0), but such a face is usually
	// a roadless island or headland, so the warning lets a replay find each
	// one the city loses.
	minKm2 := landSideWarnMin(boundary)
	landByDirection := func(i int) {
		if leftOf[i] == 0 || rightOf[i] > 0 {
			return
		}
		inside, at, ok := areaInBoundary(faces[i].polys, boundary)
		if !ok || inside <= minKm2 {
			return
		}
		logs.From(ctx).Warn("water coastline: stripped a face its coastlines put on land",
			"land_hits", hits[i], "coastlines", leftOf[i], "km2_in_boundary", inside, "at", fmt.Sprint(at))
	}
	var water []waterPolygon
	for i := range faces {
		switch h := hits[i]; {
		case h <= probes.maxHits:
			landByDirection(i)
			water = append(water, parts[i]...)
		case h <= faceSliverMaxAllowances*probes.maxHits && float64(h) <= coastlineLowSideFrac*float64(contrast[i]):
			logs.From(ctx).Warn("water coastline: stripped a face holding a few city roads beside thousands across its coastline",
				"land_hits", h, "across_land_hits", contrast[i], "allowance", probes.maxHits,
				"area_lonlat_sq", facesArea(parts[i]))
			landByDirection(i)
			water = append(water, parts[i]...)
		default:
			logs.From(ctx).Debug("water coastline: land face",
				"land_hits", h, "across_land_hits", contrast[i], "area_lonlat_sq", facesArea(parts[i]))
		}
	}
	return water
}

// splitFaces cuts the face holding sub into its parts inside sub's two
// bbox closures. sub's vertices lie inside one face (coastlines do not
// cross), but a vertex may be shared with an earlier sub, and then lies on
// two faces' boundaries; so the face is looked up with points a hair off
// sub's middle segment, and a face whose split leaves one side empty is the
// wrong one: the next candidate is tried, and a chain with none is dropped
// with a warning.
func splitFaces(ctx context.Context, faces []coastFace, sub [][2]float64, bbox [4]float64) []coastFace {
	drop := func(reason string) []coastFace {
		logs.From(ctx).Warn("water coastline: dropped chain — "+reason, "vertices", len(sub))
		return faces
	}
	head, tail := sub[0], sub[len(sub)-1]
	if !onBBoxEdge(head, bbox) || !onBBoxEdge(tail, bbox) {
		if !zeroLength(sub) { // a clip touching the bbox at one point is no chain
			logs.From(ctx).Warn("water coastline: skipped a coastline that does not reach the bbox edge at both ends",
				"vertices", len(sub), "head", fmt.Sprint(head), "tail", fmt.Sprint(tail))
		}
		return faces
	}
	ringCW, okCW := assembleClosedRing(sub, bboxWalkCW(tail, head, bbox))
	ringCCW, okCCW := assembleClosedRing(sub, bboxWalkCCW(tail, head, bbox))
	if !okCW || !okCCW {
		return faces // degenerate: both endpoints on one edge with nothing between
	}
	tried := map[int]bool{}
	for _, at := range []int{faceAt(faces, sub, 1), faceAt(faces, sub, -1)} {
		if at < 0 || tried[at] {
			continue
		}
		tried[at] = true
		cw, err := geo.IntersectLonLat(faces[at].polys, []geo.LonLatPolygon{{ringCW}})
		if err != nil {
			return drop("face split failed: " + err.Error())
		}
		ccw, err := geo.IntersectLonLat(faces[at].polys, []geo.LonLatPolygon{{ringCCW}})
		if err != nil {
			return drop("face split failed: " + err.Error())
		}
		if len(cw) == 0 || len(ccw) == 0 {
			continue // the face lies wholly on one side: not the one sub cuts
		}
		out := append([]coastFace{}, faces[:at]...)
		out = append(out, faces[at+1:]...)
		return append(out, coastFace{polys: cw}, coastFace{polys: ccw})
	}
	return drop("no face holds it")
}

// zeroLength reports a sub-chain whose vertices all coincide.
func zeroLength(sub [][2]float64) bool {
	for _, p := range sub[1:] {
		if p != sub[0] {
			return false
		}
	}
	return true
}

// faceWater is a face as water polygons, each with the islands inside it as
// further holes.
func faceWater(f coastFace, islands [][][2]float64) []waterPolygon {
	out := make([]waterPolygon, 0, len(f.polys))
	for _, p := range f.polys {
		w := withIslands(p[0], islands)
		w.holes = append(append([][][2]float64{}, p[1:]...), w.holes...)
		out = append(out, w)
	}
	return out
}

// faceAt is the index of the face just to one side of sub's middle segment
// (side 1 left, -1 right, a hair off the line), or -1.
func faceAt(faces []coastFace, sub [][2]float64, side float64) int {
	k := len(sub) / 2
	if k == len(sub)-1 {
		k--
	}
	a, b := sub[k], sub[k+1]
	dx, dy := b[0]-a[0], b[1]-a[1]
	n := math.Hypot(dx, dy)
	if n == 0 {
		return -1
	}
	const off = 1e-7 // degrees, about a centimetre
	p := [2]float64{(a[0]+b[0])/2 - side*off*dy/n, (a[1]+b[1])/2 + side*off*dx/n}
	for i, f := range faces {
		if lonLatContains(f.polys, p) {
			return i
		}
	}
	return -1
}

// startOutside rotates a closed chain that crosses the bbox edge to start
// at a vertex outside the bbox, so that clipping it gives sub-chains that
// run from edge to edge. Started inside, the clip would cut the run that
// holds the start in two, each with one end inside, and neither could cut
// a face (an island crossing the bbox edge). Other chains are returned as
// they are.
func startOutside(chain [][2]float64, bbox [4]float64) [][2]float64 {
	if !isClosedRing(chain) {
		return chain
	}
	south, west, north, east := bbox[0], bbox[1], bbox[2], bbox[3]
	for i, p := range chain[:len(chain)-1] {
		if p[0] < west || p[0] > east || p[1] < south || p[1] > north {
			if i == 0 {
				return chain
			}
			out := make([][2]float64, 0, len(chain))
			out = append(out, chain[i:len(chain)-1]...)
			out = append(out, chain[:i+1]...)
			return out
		}
	}
	return chain
}

func lonLatContains(polys []geo.LonLatPolygon, p [2]float64) bool {
	for _, poly := range polys {
		if !pointInRing(p, poly[0]) {
			continue
		}
		inHole := false
		for _, h := range poly[1:] {
			if pointInRing(p, h) {
				inHole = true
				break
			}
		}
		if !inHole {
			return true
		}
	}
	return false
}

func facesArea(parts []waterPolygon) float64 {
	var a float64
	for _, p := range parts {
		a += math.Abs(ringSignedArea(p.outer))
		for _, h := range p.holes {
			a -= math.Abs(ringSignedArea(h))
		}
	}
	return a
}

// The land-side warning (waterFaces) fires only for a face with more than
// landSideWarnMinKm2 inside the city boundary, or landSideWarnMinFrac of
// the boundary when that is less: most faces the coastlines put on land
// lie outside the boundary, in the bbox around it (the batch-1 sample's
// 34 such warnings, all but one in Juneau, San Francisco, Tampa,
// Jacksonville and Foster City, held no probe at all and changed no
// city's area), and a tiny boundary's islets still matter to it.
const (
	landSideWarnMinKm2  = 0.05
	landSideWarnMinFrac = 0.005
)

// landSideWarnMin is the in-boundary area a land-side face needs to be
// warned about.
func landSideWarnMin(boundary []geo.LonLatPolygon) float64 {
	return min(landSideWarnMinKm2, landSideWarnMinFrac*lonLatKm2(boundary))
}

// areaInBoundary is how much of a face lies inside the boundary, in km²,
// and a point of that part; ok is false with no boundary or a failed
// overlay.
func areaInBoundary(face, boundary []geo.LonLatPolygon) (float64, [2]float64, bool) {
	if len(boundary) == 0 {
		return 0, [2]float64{}, false
	}
	inside, err := geo.IntersectLonLat(face, boundary)
	if err != nil || len(inside) == 0 {
		return 0, [2]float64{}, err == nil
	}
	return lonLatKm2(inside), inside[0][0][0], true
}

// lonLatKm2 approximates the area of lon/lat polygons in km², scaling each
// ring by its latitude: enough for a warning threshold.
func lonLatKm2(polys []geo.LonLatPolygon) float64 {
	const kmPerDeg = 111.32
	var a float64
	for _, p := range polys {
		for i, r := range p {
			if len(r) == 0 {
				continue
			}
			_, minY, _, maxY := ringBBox(r)
			ra := math.Abs(ringSignedArea(r)) * kmPerDeg * kmPerDeg * math.Cos((minY+maxY)/2*math.Pi/180)
			if i == 0 {
				a += ra
			} else {
				a -= ra
			}
		}
	}
	return a
}

// boundaryLonLat reads a GeoJSON boundary as lon/lat polygons, or nil.
func boundaryLonLat(gjson string) []geo.LonLatPolygon {
	if gjson == "" {
		return nil
	}
	polys, err := parsePolygons(gjson)
	if err != nil {
		return nil
	}
	out := make([]geo.LonLatPolygon, len(polys))
	for i, p := range polys {
		out[i] = p
	}
	return out
}
