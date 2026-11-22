package services

import (
	"context"

	"github.com/jcrussell/solvent-streets/internal/geo"
)

// HexCoverage is one hex's response time for a service: the spec's turnout
// plus travel to the first road position inside the hex any facility reaches
// (first arrival anywhere in the hex). A hex with no road inside it is timed
// to the reachable road position nearest its interior point
// (geo.Hex.InteriorXY: its centre, or for a boundary-clipped hex whose centre
// fell outside, a point on what remains).
type HexCoverage struct {
	HexID    string
	Seconds  float64
	Facility int // index into the facilities passed to ComputeCoverage
}

// Coverage is the result of ComputeCoverage.
type Coverage struct {
	Hexes []HexCoverage
	// Unreachable lists the hexes on the network (a road within HexSnapM)
	// that no facility reaches: a disconnected fragment, a one-way trap, or
	// every road when no facility snapped. They are the least-covered hexes
	// there are, so the within-threshold share counts them; they carry no
	// seconds.
	Unreachable []string
	// Unsnapped lists facilities farther than FacilitySnapM from every road
	// segment, which therefore contributed nothing; callers warn about them.
	Unsnapped []int
	// Isolated lists facilities that snapped to a road fragment of fewer than
	// MinFacilityComponent vertices — mapped beside a driveway or apron that
	// OSM does not join to the street, so they reach almost nothing.
	Isolated []int
}

// ComputeCoverage routes from every facility at once (multi-source Dijkstra)
// and reports, per hex on the network, the total response seconds and which
// facility arrives first. A hex is timed to the earliest-reached position on
// any road inside it (Graph.PartsInside; motorway carriageways only pass
// through), so a driveway or private service road nearest the hex centre,
// joined to the streets far away, does not decide its time. A hex with no
// reached road inside falls back to the reachable position nearest its
// interior point (geo.Hex.InteriorXY) within HexSnapM.
// Facilities and fallback points snap to the nearest point along a road
// segment, not merely to a vertex, so a long segment with sparse vertices
// still reaches the hexes it crosses and travel is costed to the snapped
// point. Facilities snap to the street network (SnapConnected)
// rather than to a closer unjoined stub, and hexes to the nearest position a
// facility can reach; neither snaps onto a motorway carriageway unless no
// other road is in range (see SnapWhere). Hexes off the network are
// omitted; hexes on it that no facility reaches are listed in Unreachable,
// with no seconds rather than a sentinel. ctx cancellation yields partial
// lists with no error, like geo.ComputeHexStats; callers check ctx.Err()
// before persisting.
func ComputeCoverage(ctx context.Context, spec Spec, hexes []geo.Hex, g *Graph, facilities []Facility) Coverage {
	var cov Coverage
	var seeds []Seed
	// sourceSnaps holds each snapped facility's position, keyed by segment,
	// so a hex on the same segment can be reached along it directly.
	sourceSnaps := map[int][]int{}
	snaps := make([]Snap, len(facilities))
	for i, f := range facilities {
		s, ok := g.SnapConnectedLonLat(f.Lon, f.Lat, FacilitySnapM)
		if !ok {
			cov.Unsnapped = append(cov.Unsnapped, i)
			continue
		}
		if g.SnapComponentSize(s) < MinFacilityComponent {
			cov.Isolated = append(cov.Isolated, i)
		}
		snaps[i] = s
		sourceSnaps[s.Seg] = append(sourceSnaps[s.Seg], i)
		seeds = append(seeds, g.SeedsAt(s, i)...)
	}
	dist, src := g.ShortestFromSeeds(seeds)
	// arrival is the best (seconds, facility) for a network position: via
	// the end vertices, or along the segment from a facility snapped onto it.
	arrival := func(s Snap) (float64, int) {
		best, bestSrc := g.ArrivalAt(s, dist, src)
		for _, fi := range sourceSnaps[s.Seg] {
			if c := g.AlongSegment(snaps[fi], s); c < best {
				best, bestSrc = c, fi
			}
		}
		return best, bestSrc
	}
	results := geo.ParallelMap(ctx, hexes, func(_ int, h geo.Hex) []hexResult {
		r, ok := timeHex(g, h, arrival, sourceSnaps, snaps)
		if !ok {
			return nil
		}
		if r.reachable {
			r.hex.Seconds += spec.TurnoutS
		}
		return []hexResult{r}
	}, nil)
	for _, r := range results {
		if r.reachable {
			cov.Hexes = append(cov.Hexes, r.hex)
		} else {
			cov.Unreachable = append(cov.Unreachable, r.hex.HexID)
		}
	}
	return cov
}

// hexResult is one hex's outcome: timed (reachable) or on the network but
// reached by no facility.
type hexResult struct {
	hex       HexCoverage
	reachable bool
}

// timeHex is one hex's travel time (turnout not included) and facility:
// first arrival on a road inside it, else the reachable position nearest its
// interior point within HexSnapM. false when the hex is off the network.
func timeHex(g *Graph, h geo.Hex, arrival func(Snap) (float64, int), sourceSnaps map[int][]int, snaps []Snap) (hexResult, bool) {
	parts := g.PartsInside(h.Geom)
	if secs, fi, ok := firstInside(parts, arrival, sourceSnaps, snaps); ok {
		return hexResult{hex: HexCoverage{HexID: h.ID, Seconds: secs, Facility: fi}, reachable: true}, true
	}
	reachable := func(s Snap) bool {
		_, fi := arrival(s)
		return fi >= 0
	}
	x, y := h.InteriorXY()
	s, ok := g.SnapWhere(x, y, HexSnapM, reachable)
	if !ok {
		// A road inside the hex puts it on the network even when its
		// interior point is farther than HexSnapM from every road (a large
		// hex_edge_m).
		_, onNetwork := g.SnapWhere(x, y, HexSnapM, nil)
		return hexResult{hex: HexCoverage{HexID: h.ID}}, onNetwork || len(parts) > 0
	}
	secs, fi := arrival(s)
	return hexResult{hex: HexCoverage{HexID: h.ID, Seconds: secs, Facility: fi}, reachable: true}, true
}

// firstInside is the earliest arrival at any road position inside the hex
// and the facility that makes it; false when no road inside is reached. Each
// part is checked at its ends and at every facility snapped onto it, where
// the facility arrives at no travel.
func firstInside(parts []InsidePart, arrival func(Snap) (float64, int), sourceSnaps map[int][]int, snaps []Snap) (float64, int, bool) {
	best, bestSrc, found := 0.0, -1, false
	try := func(s Snap) {
		secs, fi := arrival(s)
		if fi >= 0 && (!found || secs < best) {
			best, bestSrc, found = secs, fi, true
		}
	}
	for _, p := range parts {
		try(Snap{Seg: p.Seg, T: p.T0})
		try(Snap{Seg: p.Seg, T: p.T1})
		for _, fi := range sourceSnaps[p.Seg] {
			if t := snaps[fi].T; t > p.T0 && t < p.T1 {
				try(Snap{Seg: p.Seg, T: t})
			}
		}
	}
	return best, bestSrc, found
}
