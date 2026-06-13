package resource

import (
	"context"
	"strings"

	"github.com/jcrussell/solvent-streets/internal/geo"

	"github.com/peterstace/simplefeatures/geom"
)

const (
	GeomLineString      = "LineString"
	GeomMultiLineString = "MultiLineString"
	GeomPolygon         = "Polygon"
	GeomMultiPolygon    = "MultiPolygon"
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
	// BufferFeaturesPaired parses and buffers each feature into a cleaned
	// projected polygon, keeping each input Feature paired with its buffered
	// polygon. No union or area is computed here — downstream code builds a
	// spatial index and computes coverage per-hex, avoiding a city-wide
	// UnionMany call that OOMs on large cities. Callers that need to slice the
	// buffered set later (e.g. city-only subset, per-classification cohorts)
	// can filter on Feature.Tags without re-buffering. Invalid features are
	// dropped; an empty result means no inputs survived buffering.
	BufferFeaturesPaired(ctx context.Context, features []Feature, proj *geo.UTMProjector) []BufferedFeature
	HasCohorts() bool
}

// BufferedFeature pairs a source Feature with its cleaned, projected polygon
// produced by BufferFeaturesPaired. Downstream code reuses the geom for
// index/coverage work and the Feature for jurisdiction or classification
// filtering — buffering each feature exactly once per compute run.
type BufferedFeature struct {
	Feature Feature
	Geom    geom.Geometry
}

// Geoms extracts the geom slice from a buffered-feature slice in order.
// Useful for building a GeomIndex without an extra allocation on the
// caller side.
func Geoms(bufs []BufferedFeature) []geom.Geometry {
	out := make([]geom.Geometry, len(bufs))
	for i, b := range bufs {
		out[i] = b.Geom
	}
	return out
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
		coords := extractLineCoords(g)
		if len(coords) < 2 {
			return geom.Geometry{}, false
		}
		buffered, err := geo.BufferLineString(coords, inferWidth(f.Tags))
		if err != nil {
			return geom.Geometry{}, false
		}
		return validatePolygonOK(buffered)
	case GeomMultiLineString:
		return bufferMultiLineCorridors(g, inferWidth(f.Tags))
	case GeomPolygon, GeomMultiPolygon:
		// ValidatePolygon is a Buffer(0) clean; it is dimension-agnostic and
		// fixes both Polygon and MultiPolygon (e.g. parking relations).
		return validatePolygonOK(g)
	default:
		return geom.Geometry{}, false
	}
}

// validatePolygonOK returns a topologically valid polygonal geometry, returning
// ok=false on error so call sites stay a single line. geom.Buffer already emits
// valid polygons, so already-valid input is returned untouched; only when
// Validate() reports a defect do we pay for a Buffer(0) clean (geo.ValidatePolygon),
// the documented defense against "side location conflict" precision artifacts.
//
// Skipping the Buffer(0) on already-valid input is safe because the downstream
// hex-phase overlay ops (hexCoverageArea, clipHexToCandidates) recover JTS
// topology panics into errors and skip the candidate on failure — so a residual
// precision artifact can at most drop one candidate's contribution, never crash.
func validatePolygonOK(g geom.Geometry) (geom.Geometry, bool) {
	if g.Validate() == nil {
		return g, true
	}
	cleaned, err := geo.ValidatePolygon(g)
	if err != nil {
		return geom.Geometry{}, false
	}
	return cleaned, true
}

// bufferMultiLineCorridors buffers each part of a MultiLineString separately
// (concatenating parts first would fabricate bridge segments) and unions the
// resulting corridors into one cleaned polygon.
func bufferMultiLineCorridors(g geom.Geometry, width float64) (geom.Geometry, bool) {
	mls, ok := g.AsMultiLineString()
	if !ok {
		return geom.Geometry{}, false
	}
	var parts []geom.Geometry
	for i := range mls.NumLineStrings() {
		coords := lineStringCoords(mls.LineStringN(i))
		if len(coords) < 2 {
			continue
		}
		if buffered, err := geo.BufferLineString(coords, width); err == nil {
			parts = append(parts, buffered)
		}
	}
	if len(parts) == 0 {
		return geom.Geometry{}, false
	}
	merged, err := geom.UnionMany(parts)
	if err != nil {
		return geom.Geometry{}, false
	}
	return validatePolygonOK(merged)
}

// lineStringCoords extracts the XY coordinates of a single LineString.
func lineStringCoords(ls geom.LineString) [][2]float64 {
	seq := ls.Coordinates()
	n := seq.Length()
	coords := make([][2]float64, n)
	for i := range n {
		c := seq.Get(i)
		coords[i] = [2]float64{c.X, c.Y}
	}
	return coords
}

// bufferFeaturesPaired buffers every feature through cleanFeatureGeometry in
// parallel (geo.ParallelMap, capped at NumCPU). Each feature is independent and
// pure, so this is order-preserving: ParallelMap flattens the per-feature
// slices in input order, and we emit a one-element slice for each success and an
// empty slice for each drop — yielding exactly the successes-only ordering the
// old sequential loop produced. counter is nil: buffering has no TUI phase.
func bufferFeaturesPaired(ctx context.Context, features []Feature, proj *geo.UTMProjector, inferWidth widthFunc) []BufferedFeature {
	return geo.ParallelMap(ctx, features, func(_ int, f Feature) []BufferedFeature {
		if g, ok := cleanFeatureGeometry(f, proj, inferWidth); ok {
			return []BufferedFeature{{Feature: f, Geom: g}}
		}
		return nil
	}, nil)
}
