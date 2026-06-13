package resource

import (
	"context"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/geo"
)

type Parking struct{}

func (p *Parking) Type() Type       { return TypeParking }
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

func (p *Parking) BufferFeaturesPaired(ctx context.Context, features []Feature, proj *geo.UTMProjector) []BufferedFeature {
	return geo.ParallelMap(ctx, features, func(_ int, f Feature) []BufferedFeature {
		g, gtype, err := geo.GeoJSONToProjectedGeometry(f.GeometryJSON, proj)
		if err != nil {
			return nil
		}
		if gtype != GeomPolygon {
			return nil
		}
		cleaned, ok := validatePolygonOK(g)
		if !ok {
			return nil
		}
		return []BufferedFeature{{Feature: f, Geom: cleaned}}
	}, nil)
}
