package resource

import (
	"fmt"

	"pvmt/internal/geo"

	"github.com/peterstace/simplefeatures/geom"
)

type Pavement struct{}

func (p *Pavement) Name() string { return "pavements" }

func (p *Pavement) OverpassQuery(bbox [4]float64) string {
	return fmt.Sprintf(`[out:json][timeout:120];
(
  way["highway"]["highway"!~"^(proposed|construction|bridleway|steps)$"](%f,%f,%f,%f);
);
out body;
>;
out skel qt;`, bbox[0], bbox[1], bbox[2], bbox[3])
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
			widthProjected := geo.WidthInProjectedUnits(width, proj)
			coords := extractLineCoords(g)
			if len(coords) < 2 {
				continue
			}
			buffered, err := geo.BufferLineString(coords, widthProjected)
			if err != nil {
				continue
			}
			geometries = append(geometries, buffered)
		case "Polygon":
			geometries = append(geometries, g)
		}
	}

	if len(geometries) == 0 {
		return "", 0, fmt.Errorf("no valid geometries to process")
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

func extractLineCoords(g geom.Geometry) [][2]float64 {
	ls, ok := g.AsLineString()
	if !ok {
		return nil
	}
	seq := ls.Coordinates()
	n := seq.Length()
	coords := make([][2]float64, n)
	for i := 0; i < n; i++ {
		c := seq.Get(i)
		coords[i] = [2]float64{c.X, c.Y}
	}
	return coords
}
