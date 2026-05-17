package resource

import (
	"errors"
	"strings"

	"github.com/jcrussell/solvent-streets/internal/geo"

	"github.com/peterstace/simplefeatures/geom"
)

const (
	GeomLineString = "LineString"
	GeomPolygon    = "Polygon"
)

// Type is the value stored in the resource_type TEXT column. A bare Type
// is the kind alone (e.g. "roads"); applying With(scope) appends a scope
// suffix to produce a jurisdiction-scoped label like "roads:city". Both
// forms coexist as separate rows in SQLite.
type Type string

const (
	TypeRoads     Type = "roads"
	TypeParking   Type = "parking"
	TypeSidewalks Type = "sidewalks"
	TypeCombined  Type = "combined"
)

// Scope is the geographic-extent suffix appended to a Type for
// jurisdiction-scoped rows. The empty Scope is "all" — no suffix.
type Scope string

const (
	ScopeAll  Scope = ""
	ScopeCity Scope = ":city"
)

// scopeSep is the separator between bare Type and Scope suffix.
const scopeSep = ":"

// With binds t to scope. Returns t unchanged when scope is ScopeAll.
// Panics on programmer error (binding an already-scoped value) so the
// mistake surfaces at the call site, not as a malformed "roads:city:city"
// row in the database.
func (t Type) With(scope Scope) Type {
	if strings.Contains(string(t), scopeSep) {
		panic("resource.Type.With called on already-scoped value: " + string(t))
	}
	return Type(string(t) + string(scope))
}

// Bare returns t stripped of any scope suffix.
func (t Type) Bare() Type {
	if i := strings.IndexByte(string(t), ':'); i >= 0 {
		return t[:i]
	}
	return t
}

// Scope returns the scope suffix on t, or ScopeAll if t has none.
func (t Type) Scope() Scope {
	if i := strings.IndexByte(string(t), ':'); i >= 0 {
		return Scope(t[i:])
	}
	return ScopeAll
}

// Pre-built ResourceType values for the cross-resource union rows written
// by `pvmt all compute`. Used by the compute producer and the export
// consumer; kept here so the labels are reachable without an internal →
// pkg/cmd dependency.
var (
	CombinedAll  = TypeCombined
	CombinedCity = TypeCombined.With(ScopeCity)
)

// Source is the OSM/ArcGIS data-source abstraction. Implementations
// (Pavement, Parking, Sidewalk) each expose one Type, an Overpass query
// template, a buffer function, and a HasCohorts flag.
type Source interface {
	Type() Type
	OverpassQuery(bbox [4]float64) string
	// BufferFeatures parses and buffers each feature into a cleaned projected
	// polygon, returning the slice of polygons. No union or area is computed
	// here — downstream code builds a spatial index and computes coverage
	// per-hex, avoiding a city-wide UnionMany call that OOMs on large cities.
	BufferFeatures(features []Feature, proj *geo.UTMProjector) ([]geom.Geometry, error)
	HasCohorts() bool
}

var All = []Source{
	&Pavement{},
	&Parking{},
	&Sidewalk{},
}

// ByType returns the Source whose Type matches, or nil if none does.
// Accepts only bare Types (no scope suffix).
func ByType(t Type) Source {
	for _, r := range All {
		if r.Type() == t {
			return r
		}
	}
	return nil
}

type Feature struct {
	ID           string
	Name         string
	Tags         map[string]string
	GeometryJSON string // GeoJSON geometry string
	SourceAPI    string
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
