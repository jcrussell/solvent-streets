package resource

import (
	"fmt"

	"pvmt/internal/geo"

	"github.com/peterstace/simplefeatures/geom"
)

const (
	GeomLineString = "LineString"
	GeomPolygon    = "Polygon"
)

type Feature struct {
	ID           string
	Name         string
	Tags         map[string]string
	GeometryJSON string // GeoJSON geometry string
	SourceAPI    string
}

type ResourceType interface {
	Name() string
	OverpassQuery(bbox [4]float64) string
	ProcessFeatures(features []Feature, proj geo.Projector) (string, float64, error) // returns (unionGeoJSON, areaSqM, error)
	HasCohorts() bool                                                                // whether this resource type supports per-classification cohort stats
}

var All = []ResourceType{
	&Pavement{},
	&Parking{},
	&Sidewalk{},
}

func ByName(name string) ResourceType {
	for _, r := range All {
		if r.Name() == name {
			return r
		}
	}
	return nil
}

type widthFunc func(tags map[string]string) float64

// cleanFeatureGeometry converts a single feature to a cleaned projected geometry.
// For LineStrings, it buffers by the inferred width. Returns (geometry, ok).
func cleanFeatureGeometry(f Feature, proj geo.Projector, inferWidth widthFunc) (geom.Geometry, bool) {
	g, gtype, err := geo.GeoJSONToProjectedGeometry(f.GeometryJSON, proj)
	if err != nil {
		return geom.Geometry{}, false
	}

	switch gtype {
	case GeomLineString:
		width := inferWidth(f.Tags)
		coords := extractLineCoords(g)
		if len(coords) < 2 {
			return geom.Geometry{}, false
		}
		buffered, err := geo.BufferLineString(coords, width)
		if err != nil {
			return geom.Geometry{}, false
		}
		cleaned, err := geo.ValidatePolygon(buffered)
		if err != nil {
			return geom.Geometry{}, false
		}
		return cleaned, true
	case GeomPolygon:
		cleaned, err := geo.ValidatePolygon(g)
		if err != nil {
			return geom.Geometry{}, false
		}
		return cleaned, true
	default:
		return geom.Geometry{}, false
	}
}

func processFeatures(features []Feature, proj geo.Projector, inferWidth widthFunc) (string, float64, error) {
	var geometries []geom.Geometry

	for _, f := range features {
		if g, ok := cleanFeatureGeometry(f, proj, inferWidth); ok {
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

	areaSqM := geo.AreaInProjectedUnits(union)
	gjson, err := geo.GeometryToGeoJSON(union, proj)
	if err != nil {
		return "", areaSqM, fmt.Errorf("to geojson: %w", err)
	}

	return gjson, areaSqM, nil
}
