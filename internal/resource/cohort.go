package resource

import (
	"pvmt/internal/forecast"
	"pvmt/internal/geo"

	"github.com/peterstace/simplefeatures/geom"
)

// ComputeRoadCohortAreas computes per-classification union areas by buffering
// each feature, grouping by classification, and unioning within each class.
// This avoids inflating class areas due to intra-class overlaps.
// Returns map[classification]unionAreaSqM.
func ComputeRoadCohortAreas(features []Feature, proj geo.Projector) map[string]float64 {
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
		u, err := geo.UnionAll(geoms)
		if err != nil {
			// Fallback: sum individual areas if union fails
			var sum float64
			for _, g := range geoms {
				sum += geo.AreaInProjectedUnits(g)
			}
			areas[class] = sum
			continue
		}
		areas[class] = geo.AreaInProjectedUnits(u)
	}
	return areas
}
