package resource

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"

	"github.com/jcrussell/solvent-streets/internal/geo"

	"github.com/peterstace/simplefeatures/geom"
)

const (
	GeomLineString = "LineString"
	GeomPolygon    = "Polygon"
)

// Kind enumerates the canonical resource kinds stored in the resource_type
// column. KindRoads/KindParking/KindSidewalks correspond 1:1 with the Source
// implementations; KindCombined is the cross-resource union written by
// `pvmt all compute`. Stored as text via ResourceType, which composes Kind
// with Scope and implements sql.Scanner / driver.Valuer.
type Kind int

const (
	KindUnknown Kind = iota
	KindRoads
	KindParking
	KindSidewalks
	KindCombined
)

// String returns the canonical column value for a Kind. KindUnknown maps
// to "unknown"; ResourceType.Value refuses to write that value so it
// never lands in the database.
func (k Kind) String() string {
	switch k {
	case KindUnknown:
		return "unknown"
	case KindRoads:
		return "roads"
	case KindParking:
		return "parking"
	case KindSidewalks:
		return "sidewalks"
	case KindCombined:
		return "combined"
	default:
		return "unknown"
	}
}

// ParseKind parses the canonical column value for a Kind. Returns
// KindUnknown plus a non-nil error for unrecognized inputs so schema
// drift surfaces at the read boundary via ResourceType.Scan.
func ParseKind(s string) (Kind, error) {
	switch s {
	case "roads":
		return KindRoads, nil
	case "parking":
		return KindParking, nil
	case "sidewalks":
		return KindSidewalks, nil
	case "combined":
		return KindCombined, nil
	}
	return KindUnknown, fmt.Errorf("unknown resource kind %q", s)
}

// WithScope binds a Kind to a Scope, producing the ResourceType value
// that gets stored in (and queried from) the resource_type column.
func (k Kind) WithScope(s Scope) ResourceType {
	return ResourceType{Kind: k, Scope: s}
}

// Scope distinguishes the geographic extent of a stored row. Compute
// emits two passes per source: ScopeAll over the bbox-clipped feature
// set, and ScopeCity intersected with the city boundary. The two
// coexist as separate rows tagged by their Scope.
type Scope int

const (
	ScopeAll Scope = iota
	ScopeCity
)

// String returns the column-format suffix for a Scope: "" for ScopeAll,
// ":city" for ScopeCity.
func (s Scope) String() string {
	if s == ScopeCity {
		return ":city"
	}
	return ""
}

// ResourceType is the value of the resource_type TEXT column. Implements
// sql.Scanner and driver.Valuer so database/sql converts to and from the
// canonical "<kind>" / "<kind>:city" wire format automatically. The zero
// value is {KindUnknown, ScopeAll}; Value refuses to marshal it so an
// uninitialized struct cannot land in the database.
//
// String/Value/MarshalJSON take value receivers (read-only, map-key callable);
// Scan/UnmarshalJSON must be pointer receivers to mutate the receiver.
//
//nolint:recvcheck // pointer methods (Scan/UnmarshalJSON) must mutate; value methods (String/Value/MarshalJSON) must be callable on non-addressable map keys.
type ResourceType struct {
	Kind  Kind
	Scope Scope
}

// String returns the canonical wire format: "<kind>" for ScopeAll or
// "<kind>:city" for ScopeCity.
func (rt ResourceType) String() string {
	return rt.Kind.String() + rt.Scope.String()
}

// Value implements driver.Valuer.
func (rt ResourceType) Value() (driver.Value, error) {
	if rt.Kind == KindUnknown {
		return nil, errors.New("refusing to write unknown resource_type")
	}
	return rt.String(), nil
}

// MarshalJSON emits the canonical string form. Keeps the JSON contract
// stable across the Kind-iota rename — clients see "roads", not the
// underlying struct.
func (rt ResourceType) MarshalJSON() ([]byte, error) {
	if rt.Kind == KindUnknown {
		return []byte(`""`), nil
	}
	return []byte(`"` + rt.String() + `"`), nil
}

// UnmarshalJSON parses the canonical string form. Round-trips with
// MarshalJSON; rejects values that don't parse as <kind>[:city].
func (rt *ResourceType) UnmarshalJSON(data []byte) error {
	if len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' {
		return fmt.Errorf("resource_type json: not a string: %s", data)
	}
	s := string(data[1 : len(data)-1])
	if s == "" {
		*rt = ResourceType{}
		return nil
	}
	return rt.Scan(s)
}

// Scan implements sql.Scanner. Accepts the canonical column format
// "<kind>" or "<kind>:city"; returns an error for anything else.
func (rt *ResourceType) Scan(src any) error {
	var s string
	switch v := src.(type) {
	case string:
		s = v
	case []byte:
		s = string(v)
	case nil:
		return errors.New("scan nil into resource_type")
	default:
		return fmt.Errorf("scan %T into resource_type", src)
	}
	bare, scope := s, ScopeAll
	if rest, ok := strings.CutSuffix(s, ":city"); ok {
		bare = rest
		scope = ScopeCity
	}
	k, err := ParseKind(bare)
	if err != nil {
		return fmt.Errorf("scan resource_type: %w", err)
	}
	rt.Kind = k
	rt.Scope = scope
	return nil
}

// CombinedAll and CombinedCity are pre-constructed ResourceType values
// for the cross-resource union rows. Used by `pvmt all compute` (producer)
// and internal/export (consumer); kept here so the labels are reachable
// without an internal → pkg/cmd dependency.
var (
	CombinedAll  = KindCombined.WithScope(ScopeAll)
	CombinedCity = KindCombined.WithScope(ScopeCity)
)

// Source is the OSM/ArcGIS data-source abstraction. Implementations
// (Pavement, Parking, Sidewalk) each expose one Kind, an Overpass query
// template, a buffer function, and a HasCohorts flag.
type Source interface {
	Kind() Kind
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

// ByKind returns the Source whose Kind matches, or nil if none does.
func ByKind(k Kind) Source {
	for _, r := range All {
		if r.Kind() == k {
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
