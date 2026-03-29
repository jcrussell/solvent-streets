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
		g, gtype, err := geo.GeoJSONToProjectedGeometry(f.GeometryJSON, proj)
		if err != nil {
			continue
		}

		var cleaned geom.Geometry
		switch gtype {
		case "LineString":
			width := geo.InferWidth(f.Tags)
			coords := extractLineCoords(g)
			if len(coords) < 2 {
				continue
			}
			buffered, err := geo.BufferLineString(coords, width)
			if err != nil {
				continue
			}
			cleaned, err = geo.ValidatePolygon(buffered)
			if err != nil {
				continue
			}
		case "Polygon":
			cleaned, err = geo.ValidatePolygon(g)
			if err != nil {
				continue
			}
		default:
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
