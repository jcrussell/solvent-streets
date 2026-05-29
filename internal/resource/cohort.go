package resource

import (
	"context"

	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/geo"

	"github.com/peterstace/simplefeatures/geom"
)

// ComputeRoadCohortAreas computes per-classification coverage areas from
// already-buffered features by grouping each feature's polygon by highway
// class and running the same R-tree + per-hex local-union pipeline that
// ComputeHexStats uses against the supplied clipped hex grid. Buffering
// happens once at the caller; this function only indexes and intersects.
// Intra-class overlaps are dedup'd per-hex rather than via one big
// UnionAll, and per-class totals are clipped to the same hex grid as the
// "all" total so they sum consistently. Returns
// map[classification]coverageArea. ctx cancellation aborts the
// underlying ParallelMap calls cleanly.
func ComputeRoadCohortAreas(ctx context.Context, buffered []BufferedFeature, hexes []geo.Hex) map[string]float64 {
	classGeoms := make(map[string][]geom.Geometry)

	for _, bf := range buffered {
		class := forecast.NormalizeClass(bf.Feature.Tags["highway"])
		classGeoms[class] = append(classGeoms[class], bf.Geom)
	}

	areas := make(map[string]float64)
	for class, geoms := range classGeoms {
		idx := geo.NewGeomIndexFromGeoms(geoms)
		stats := geo.ComputeHexStats(ctx, hexes, idx, class, nil)
		var sum float64
		for _, s := range stats {
			sum += s.Area
		}
		areas[class] = sum
	}
	return areas
}
