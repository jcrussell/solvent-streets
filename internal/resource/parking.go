package resource

import (
	"errors"
	"fmt"

	"pvmt/internal/geo"

	"github.com/peterstace/simplefeatures/geom"
)

type Parking struct{}

func (p *Parking) Name() string     { return "parking" }
func (p *Parking) HasCohorts() bool { return false }

func (p *Parking) OverpassQuery(bbox [4]float64) string {
	return fmt.Sprintf(`[out:json][timeout:120];
(
  way["amenity"="parking"](%f,%f,%f,%f);
  relation["amenity"="parking"](%f,%f,%f,%f);
);
out geom;`, bbox[0], bbox[1], bbox[2], bbox[3],
		bbox[0], bbox[1], bbox[2], bbox[3])
}

func (p *Parking) BufferFeatures(features []Feature, proj *geo.UTMProjector) ([]geom.Geometry, error) {
	geometries := make([]geom.Geometry, 0, len(features))
	for _, f := range features {
		g, gtype, err := geo.GeoJSONToProjectedGeometry(f.GeometryJSON, proj)
		if err != nil {
			continue
		}
		if gtype != GeomPolygon {
			continue
		}
		cleaned, err := geo.ValidatePolygon(g)
		if err != nil {
			continue
		}
		geometries = append(geometries, cleaned)
	}
	if len(geometries) == 0 {
		return nil, errors.New("no valid polygon geometries to process")
	}
	return geometries, nil
}
