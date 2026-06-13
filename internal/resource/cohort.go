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
// hex's candidates by class, and accumulates per-class coverage. This replaces
// the former per-class loop that traversed the whole grid once per class.
// Buffering happens once at the caller; this function only indexes and
// intersects. Intra-class overlaps are dedup'd per-hex rather than via one big
// UnionAll, and per-class totals are clipped to the same hex grid as the "all"
// total so they sum consistently. The returned map has one key per distinct
// classification present in buffered, including classes whose features cover no
// clipped hex (those map to 0.0) — so every class yields a persisted cohort row
// and feature count downstream. Returns map[classification]coverageArea. ctx
// cancellation aborts the underlying ParallelMap cleanly.
func ComputeRoadCohortAreas(ctx context.Context, buffered []BufferedFeature, hexes []geo.Hex) map[string]float64 {
	geoms := make([]geom.Geometry, 0, len(buffered))
	labels := make([]string, 0, len(buffered))
	for _, bf := range buffered {
		geoms = append(geoms, bf.Geom)
		labels = append(labels, forecast.NormalizeClass(bf.Feature.Tags["highway"]))
	}
	return geo.ComputeHexCoverageByGroup(ctx, hexes, geoms, labels, nil)
}
