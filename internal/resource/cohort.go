package resource

import (
	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/geo"

	"github.com/peterstace/simplefeatures/geom"
)

// ComputeRoadCohortAreas computes per-classification coverage areas by
// buffering each feature, grouping by classification, and running the same
// R-tree + per-hex local-union pipeline that ComputeHexStats uses against
// the supplied clipped hex grid. This matches the main compute pipeline —
// intra-class overlaps are dedup'd per-hex rather than via one big UnionAll,
// and per-class totals are clipped to the same hex grid as the "all" total
// so they sum consistently. Returns map[classification]coverageAreaSqM.
func ComputeRoadCohortAreas(features []Feature, proj *geo.UTMProjector, hexes []geo.Hex) map[string]float64 {
	classGeoms := make(map[string][]geom.Geometry)

	for _, f := range features {
		cleaned, ok := cleanFeatureGeometry(f, proj, geo.InferWidth)
		if !ok {
			continue
		}
		class := forecast.NormalizeClass(f.Tags["highway"])
		classGeoms[class] = append(classGeoms[class], cleaned)
	}

	areas := make(map[string]float64)
	for class, geoms := range classGeoms {
		idx := geo.NewGeomIndexFromGeoms(geoms)
		stats := geo.ComputeHexStats(hexes, idx, class, nil)
		var sum float64
		for _, s := range stats {
			sum += s.AreaSqM
		}
		areas[class] = sum
	}
	return areas
}
