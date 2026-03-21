package resource

import (
	"pvmt/internal/forecast"
	"pvmt/internal/geo"
)

// ComputeRoadCohortAreas computes raw per-classification areas by buffering
// each LineString feature individually. Returns map[classification]rawAreaSqFt.
// The caller should distribute proportionally against the union total to avoid
// overlap double-counting.
func ComputeRoadCohortAreas(features []Feature, proj geo.Projector) map[string]float64 {
	areas := make(map[string]float64)

	for _, f := range features {
		g, gtype, err := geo.GeoJSONToProjectedGeometry(f.GeometryJSON, proj)
		if err != nil {
			continue
		}

		var area float64
		switch gtype {
		case "LineString":
			width := geo.InferWidth(f.Tags)
			widthProjected := geo.WidthInProjectedUnits(width, proj)
			coords := extractLineCoords(g)
			if len(coords) < 2 {
				continue
			}
			buffered, err := geo.BufferLineString(coords, widthProjected)
			if err != nil {
				continue
			}
			cleaned, err := geo.ValidatePolygon(buffered)
			if err != nil {
				continue
			}
			area = geo.AreaSqFtFromProjected(geo.AreaInProjectedUnits(cleaned), proj)
		case "Polygon":
			cleaned, err := geo.ValidatePolygon(g)
			if err != nil {
				continue
			}
			area = geo.AreaSqFtFromProjected(geo.AreaInProjectedUnits(cleaned), proj)
		default:
			continue
		}

		class := forecast.NormalizeClass(f.Tags["highway"])
		areas[class] += area
	}

	return areas
}
