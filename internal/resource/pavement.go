package resource

import (
	"context"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/geo"

	"github.com/peterstace/simplefeatures/geom"
)

type Pavement struct{}

func (p *Pavement) Type() Type       { return TypeRoads }
func (p *Pavement) HasCohorts() bool { return true }

func (p *Pavement) OverpassQuery(bbox [4]float64) string {
	return fmt.Sprintf(`[out:json][timeout:120];
(
  way["highway"]["highway"!~"^(proposed|construction|bridleway|steps|footway|cycleway|path|track|pedestrian|corridor)$"](%f,%f,%f,%f);
);
out geom;`, bbox[0], bbox[1], bbox[2], bbox[3])
}

func (p *Pavement) BufferFeaturesPaired(ctx context.Context, features []Feature, proj *geo.UTMProjector) []BufferedFeature {
	return bufferFeaturesPaired(ctx, features, proj, geo.InferWidth)
}

func extractLineCoords(g geom.Geometry) [][2]float64 {
	ls, ok := g.AsLineString()
	if !ok {
		return nil
	}
	return lineStringCoords(ls)
}
