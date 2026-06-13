package resource

import (
	"context"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/geo"
)

type Sidewalk struct{}

func (s *Sidewalk) Type() Type       { return TypeSidewalks }
func (s *Sidewalk) HasCohorts() bool { return false }

func (s *Sidewalk) OverpassQuery(bbox [4]float64) string {
	return fmt.Sprintf(`[out:json][timeout:120];
(
  way["footway"="sidewalk"](%f,%f,%f,%f);
);
out geom;`, bbox[0], bbox[1], bbox[2], bbox[3])
}

func (s *Sidewalk) BufferFeaturesPaired(ctx context.Context, features []Feature, proj *geo.UTMProjector) []BufferedFeature {
	return bufferFeaturesPaired(ctx, features, proj, geo.InferSidewalkWidth)
}
