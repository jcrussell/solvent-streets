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

func (p *Pavement) ProcessFeatures(features []Feature, proj geo.Projector) (string, float64, error) {
	var geometries []geom.Geometry

	for _, f := range features {
		g, gtype, err := geo.GeoJSONToProjectedGeometry(f.GeometryJSON, proj)
		if err != nil {
			continue
		}

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
			cleaned, err := geo.ValidatePolygon(buffered)
			if err != nil {
				continue
			}
			geometries = append(geometries, cleaned)
		case "Polygon":
			cleaned, err := geo.ValidatePolygon(g)
			if err != nil {
				continue
			}
			geometries = append(geometries, cleaned)
		}
	}

	if len(geometries) == 0 {
		return "", 0, fmt.Errorf("no valid geometries to process")
	}

	union, err := geo.UnionAll(geometries)
	if err != nil {
		return "", 0, fmt.Errorf("union: %w", err)
	}

	areaSqM := geo.AreaInProjectedUnits(union)
	gjson, err := geo.GeometryToGeoJSON(union, proj)
	if err != nil {
		return "", areaSqM, fmt.Errorf("to geojson: %w", err)
	}

	return gjson, areaSqM, nil
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
