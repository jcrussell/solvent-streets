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
}

// ComputeCoverage routes from every facility at once (multi-source Dijkstra)
// and reports, per hex whose centre lies within HexSnapM of the network, the
// total response seconds and which facility is nearest. Hexes off the
// network or unreachable from every facility are omitted — they carry no
// value rather than a sentinel. ctx cancellation yields a partial Hexes list
// with no error, like geo.ComputeHexStats; callers check ctx.Err() before
// persisting.
func ComputeCoverage(ctx context.Context, spec Spec, hexes []geo.Hex, g *Graph, facilities []Facility) Coverage {
	var cov Coverage
	var sources []int
	var sourceFacility []int
	for i, f := range facilities {
		n, ok := g.NearestLonLat(f.Lon, f.Lat, FacilitySnapM)
		if !ok {
			cov.Unsnapped = append(cov.Unsnapped, i)
			continue
		}
		sources = append(sources, n)
		sourceFacility = append(sourceFacility, i)
	}
	if len(sources) == 0 {
		return cov
	}
	dist, src := g.ShortestFrom(sources)
	cov.Hexes = geo.ParallelMap(ctx, hexes, func(_ int, h geo.Hex) []HexCoverage {
		n, ok := g.Nearest(h.CenterX, h.CenterY, HexSnapM)
		if !ok || src[n] < 0 {
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
