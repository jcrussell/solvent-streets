package resource

import (
	"context"

	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/geo"

	"github.com/peterstace/simplefeatures/geom"
)

// ComputeRoadCohortAreas computes per-classification coverage areas from
// already-buffered features by labelling each feature's polygon with its
// highway class and running geo.ComputeHexCoverageByGroup — a SINGLE pass over
// the clipped hex grid that searches one combined index per hex, groups that
// hex's candidates by class, and accumulates per-class coverage, rather than
// traversing the whole grid once per class.
// Buffering happens once at the caller; this function only indexes and
// intersects. Intra-class overlaps are dedup'd per-hex rather than via one big
// UnionAll, and per-class totals are clipped to the same hex grid as the "all"
// total so they sum consistently. The returned map has one key per distinct
// classification present in buffered, including classes whose features cover no
// clipped hex (those map to 0.0) — so every class yields a persisted cohort row
// and feature count downstream. Returns map[classification]coverageArea. ctx
// cancellation aborts the underlying ParallelMap cleanly.
func ComputeRoadCohortAreas(ctx context.Context, buffered []BufferedFeature, hexes []geo.Hex) map[string]float64 {
	areas, _ := ComputeRoadCohortAreasPerHex(ctx, buffered, hexes)
	return areas
}

// ComputeRoadCohortAreasPerHex is ComputeRoadCohortAreas that also returns
// each hex's per-class areas (geo.ComputeHexCoverageByGroupPerHex), from the
// same single pass.
func ComputeRoadCohortAreasPerHex(ctx context.Context, buffered []BufferedFeature, hexes []geo.Hex) (map[string]float64, []geo.HexGroupArea) {
	geoms := make([]geom.Geometry, 0, len(buffered))
	labels := make([]string, 0, len(buffered))
	for _, bf := range buffered {
		geoms = append(geoms, bf.Geom)
		labels = append(labels, forecast.NormalizeClass(bf.Feature.Tags["highway"]))
	}
	return geo.ComputeHexCoverageByGroupPerHex(ctx, hexes, geoms, labels, nil)
}

// HexDecayRate is one hex's road decay rate, per year.
type HexDecayRate struct {
	HexID string
	K     float64
}

// DefaultDecayBlends folds per-hex class areas into each hex's default-rate
// blend of road class decay rates, weighted by in-hex class area:
//
//	k0 = Σ_c(area_c · ClassDecayRate(c, 0)) / Σ_c area_c
//
// so a hex of arterials holds while one of residential and service streets
// rots first. It is the rate at no decay_rate override: every road class
// scales linearly in the override (forecast.ScaleRoadDecay), so the blend does
// too, and a reader applies the override to k0 itself. perHex must keep each
// hex's entries together, as geo.ComputeHexCoverageByGroupPerHex returns them;
// hexes with no positive area are omitted. Output follows perHex's hex order.
func DefaultDecayBlends(perHex []geo.HexGroupArea) []HexDecayRate {
	var out []HexDecayRate
	for i := 0; i < len(perHex); {
		id := perHex[i].HexID
		var area, weighted float64
		for ; i < len(perHex) && perHex[i].HexID == id; i++ {
			area += perHex[i].Area
			weighted += perHex[i].Area * forecast.ClassDecayRate(perHex[i].Label, 0)
		}
		if area > 0 {
			out = append(out, HexDecayRate{HexID: id, K: forecast.ResolveDecayRate(weighted / area)})
		}
	}
	return out
}
