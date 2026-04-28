package resource

import (
	"fmt"

	"pvmt/internal/geo"

	"github.com/peterstace/simplefeatures/geom"
)

type Sidewalk struct{}

func (s *Sidewalk) Name() string     { return "sidewalks" }
func (s *Sidewalk) HasCohorts() bool { return false }

func (s *Sidewalk) OverpassQuery(bbox [4]float64) string {
	return fmt.Sprintf(`[out:json][timeout:120];
(
  way["footway"="sidewalk"](%f,%f,%f,%f);
);
out geom;`, bbox[0], bbox[1], bbox[2], bbox[3])
}

func (s *Sidewalk) BufferFeatures(features []Feature, proj *geo.UTMProjector) ([]geom.Geometry, error) {
	return bufferFeatures(features, proj, geo.InferSidewalkWidth)
}
