package resource

import (
	"fmt"

	"pvmt/internal/geo"

	"github.com/peterstace/simplefeatures/geom"
)

type Parking struct{}

func (p *Parking) Name() string { return "parking" }

func (p *Parking) OverpassQuery(bbox [4]float64) string {
	return fmt.Sprintf(`[out:json][timeout:120];
(
  way["amenity"="parking"](%f,%f,%f,%f);
  relation["amenity"="parking"](%f,%f,%f,%f);
);
out body;
>;
out skel qt;`, bbox[0], bbox[1], bbox[2], bbox[3],
		bbox[0], bbox[1], bbox[2], bbox[3])
}

func (p *Parking) ProcessFeatures(features []Feature, proj geo.Projector) (string, float64, error) {
	var geometries []geom.Geometry

	for _, f := range features {
		g, gtype, err := geo.GeoJSONToProjectedGeometry(f.GeometryJSON, proj)
		if err != nil {
			continue
		}

		if gtype == "Polygon" {
			cleaned, err := geo.ValidatePolygon(g)
			if err != nil {
				continue
			}
			geometries = append(geometries, cleaned)
		}
	}

	if len(geometries) == 0 {
		return "", 0, fmt.Errorf("no valid polygon geometries to process")
	}

	union, err := geo.UnionAll(geometries)
	if err != nil {
		return "", 0, fmt.Errorf("union: %w", err)
	}

	areaProjected := geo.AreaInProjectedUnits(union)
	areaSqFt := geo.AreaSqFtFromProjected(areaProjected, proj)
	gjson, err := geo.GeometryToGeoJSON(union, proj)
	if err != nil {
		return "", areaSqFt, fmt.Errorf("to geojson: %w", err)
	}

	return gjson, areaSqFt, nil
}
