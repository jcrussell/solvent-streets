package resource

import (
	"errors"

	"pvmt/internal/geo"

	"github.com/peterstace/simplefeatures/geom"
)

const (
	GeomLineString = "LineString"
	GeomPolygon    = "Polygon"
)

// CombinedAll and CombinedCity are the resource_type row labels for the
// cross-resource union written by `pvmt all compute`'s combined pass.
// Producer: pkg/cmd/compute. Consumers: internal/export, anything that
// reads ComputeResult rows. Kept here (not in pkg/cmd/compute) so the
// label is reachable without an internal → pkg/cmd dependency.
const (
	CombinedAll  = "combined"
	CombinedCity = "combined:city"
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
	// BufferFeatures parses and buffers each feature into a cleaned projected
	// polygon, returning the slice of polygons. No union or area is computed
	// here — downstream code builds a spatial index and computes coverage
	// per-hex, avoiding a city-wide UnionMany call that OOMs on large cities.
	BufferFeatures(features []Feature, proj *geo.UTMProjector) ([]geom.Geometry, error)
	HasCohorts() bool // whether this resource type supports per-classification cohort stats
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
func cleanFeatureGeometry(f Feature, proj *geo.UTMProjector, inferWidth widthFunc) (geom.Geometry, bool) {
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

func bufferFeatures(features []Feature, proj *geo.UTMProjector, inferWidth widthFunc) ([]geom.Geometry, error) {
	geometries := make([]geom.Geometry, 0, len(features))
	for _, f := range features {
		if g, ok := cleanFeatureGeometry(f, proj, inferWidth); ok {
			geometries = append(geometries, g)
		}
	}
	if len(geometries) == 0 {
		return nil, errors.New("no valid geometries to process")
	}
	return geometries, nil
}
