package resource

import (
	"context"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/geo"
)

type Parking struct{}

func (p *Parking) Type() Type            { return TypeParking }
func (p *Parking) HasCohorts() bool      { return false }
func (p *Parking) AsphaltSurfaced() bool { return true }

func (p *Parking) OverpassQuery(bbox [4]float64) string {
	return fmt.Sprintf(`[out:json][timeout:%d];
(
  way["amenity"="parking"](%f,%f,%f,%f);
  relation["amenity"="parking"](%f,%f,%f,%f);
);
out geom;`, OverpassTimeoutS, bbox[0], bbox[1], bbox[2], bbox[3],
		bbox[0], bbox[1], bbox[2], bbox[3])
}

// BufferFeaturesPaired cleans each lot polygon without buffering (unlike
// roads and sidewalks, a lot's footprint is its drawn outline).
func (p *Parking) BufferFeaturesPaired(ctx context.Context, features []Feature, proj geo.Projector) []BufferedFeature {
	return cleanPolygonsPaired(ctx, features, proj)
}
