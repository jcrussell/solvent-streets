package resource

import (
	"fmt"

	"pvmt/internal/geo"

	"github.com/peterstace/simplefeatures/geom"
)

type Pavement struct{}

func (p *Pavement) Name() string     { return "roads" }
func (p *Pavement) HasCohorts() bool { return true }

func (p *Pavement) OverpassQuery(bbox [4]float64) string {
	return fmt.Sprintf(`[out:json][timeout:120];
(
  way["highway"]["highway"!~"^(proposed|construction|bridleway|steps|footway|cycleway|path|track|pedestrian|corridor)$"](%f,%f,%f,%f);
);
out geom;`, bbox[0], bbox[1], bbox[2], bbox[3])
}

func (p *Pavement) BufferFeatures(features []Feature, proj geo.Projector) ([]geom.Geometry, error) {
	return bufferFeatures(features, proj, geo.InferWidth)
}

func extractLineCoords(g geom.Geometry) [][2]float64 {
	ls, ok := g.AsLineString()
	if !ok {
		return nil
	}
	seq := ls.Coordinates()
	n := seq.Length()
	coords := make([][2]float64, n)
	for i := range n {
		c := seq.Get(i)
		coords[i] = [2]float64{c.X, c.Y}
	}
	return coords
}
