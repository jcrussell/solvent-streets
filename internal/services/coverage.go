package services

import (
	"context"

	"github.com/jcrussell/solvent-streets/internal/geo"
)

// HexCoverage is one hex's response time for a service: the spec's turnout
// plus travel from the nearest reachable facility to the road vertex nearest
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
// and reports, per hex whose centre lies within HexSnapM of a REACHABLE road
// vertex, the total response seconds and which facility is nearest.
// Facilities snap to the street network (NearestConnected) rather than to a
// closer unjoined stub, and hexes to the nearest vertex a facility can reach.
// Hexes off the network or unreachable from every facility are omitted —
// they carry no value rather than a sentinel. ctx cancellation yields a
// partial Hexes list with no error, like geo.ComputeHexStats; callers check
// ctx.Err() before persisting.
func ComputeCoverage(ctx context.Context, spec Spec, hexes []geo.Hex, g *Graph, facilities []Facility) Coverage {
	var cov Coverage
	var sources []int
	var sourceFacility []int
	for i, f := range facilities {
		n, ok := g.NearestConnectedLonLat(f.Lon, f.Lat, FacilitySnapM)
		if !ok {
			cov.Unsnapped = append(cov.Unsnapped, i)
			continue
		}
		if g.ComponentSize(n) < MinFacilityComponent {
			cov.Isolated = append(cov.Isolated, i)
		}
		sources = append(sources, n)
		sourceFacility = append(sourceFacility, i)
	}
	if len(sources) == 0 {
		return cov
	}
	dist, src := g.ShortestFrom(sources)
	reachable := func(n int) bool { return src[n] >= 0 }
	cov.Hexes = geo.ParallelMap(ctx, hexes, func(_ int, h geo.Hex) []HexCoverage {
		n, ok := g.NearestWhere(h.CenterX, h.CenterY, HexSnapM, reachable)
		if !ok {
			return nil
		}
		return []HexCoverage{{
			HexID:    h.ID,
			Seconds:  spec.TurnoutS + dist[n],
			Facility: sourceFacility[src[n]],
		}}
	}, nil)
	return cov
}
