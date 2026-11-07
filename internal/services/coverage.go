package services

import (
	"context"

	"github.com/jcrussell/solvent-streets/internal/geo"
)

// HexCoverage is one hex's response time for a service: the spec's turnout
// plus travel from the nearest reachable facility to the road position nearest
// the hex centre.
type HexCoverage struct {
	HexID    string
	Seconds  float64
	Facility int // index into the facilities passed to ComputeCoverage
}

// Coverage is the result of ComputeCoverage.
type Coverage struct {
	Hexes []HexCoverage
	// Unsnapped lists facilities farther than FacilitySnapM from any road
	// vertex, which therefore contributed nothing; callers warn about them.
	Unsnapped []int
	// Isolated lists facilities that snapped to a road fragment of fewer than
	// MinFacilityComponent vertices — mapped beside a driveway or apron that
	// OSM does not join to the street, so they reach almost nothing.
	Isolated []int
}

// ComputeCoverage routes from every facility at once (multi-source Dijkstra)
// and reports, per hex whose centre lies within HexSnapM of a REACHABLE
// position on the road network, the total response seconds and which
// facility is nearest. Both facilities and hex centres snap to the nearest
// point along a road segment, not merely to a vertex, so a long segment with
// sparse vertices still reaches the hexes it crosses and travel is costed to
// the snapped point. Facilities snap to the street network (SnapConnected)
// rather than to a closer unjoined stub, and hexes to the nearest position a
// facility can reach; neither snaps onto a motorway carriageway unless no
// other road is in range (see SnapWhere). Hexes off the network or unreachable from every
// facility are omitted — they carry no value rather than a sentinel. ctx
// cancellation yields a partial Hexes list with no error, like
// geo.ComputeHexStats; callers check ctx.Err() before persisting.
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
	if len(sourceSnaps) == 0 {
		return cov
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
	reachable := func(s Snap) bool {
		_, fi := arrival(s)
		return fi >= 0
	}
	cov.Hexes = geo.ParallelMap(ctx, hexes, func(_ int, h geo.Hex) []HexCoverage {
		s, ok := g.SnapWhere(h.CenterX, h.CenterY, HexSnapM, reachable)
		if !ok {
			return nil
		}
		secs, fi := arrival(s)
		return []HexCoverage{{HexID: h.ID, Seconds: spec.TurnoutS + secs, Facility: fi}}
	}, nil)
	return cov
}
