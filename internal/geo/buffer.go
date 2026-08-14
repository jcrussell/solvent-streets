package geo

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/peterstace/simplefeatures/geom"
)

// BufferLineString buffers a projected linestring by width/2 with flat end caps.
// Coordinates must already be in the projected coordinate system.
func BufferLineString(coords [][2]float64, widthProjected float64) (geom.Geometry, error) {
	if len(coords) < 2 {
		return geom.Geometry{}, errors.New("need at least 2 coordinates")
	}
	// A non-positive or NaN width makes geom.Buffer(width/2) either produce an
	// empty/degenerate corridor or return an opaque error; reject it up front so
	// callers get a clear signal instead of silently losing the feature.
	if math.IsNaN(widthProjected) || widthProjected <= 0 {
		return geom.Geometry{}, fmt.Errorf("width must be positive and finite, got %v", widthProjected)
	}
	seq := coordsToSequence(coords)
	ls := geom.NewLineString(seq)
	g := ls.AsGeometry()
	buffered, err := geom.Buffer(g, widthProjected/2, geom.BufferEndCapFlat(), geom.BufferQuadSegments(8))
	if err != nil {
		return geom.Geometry{}, fmt.Errorf("buffer: %w", err)
	}
	return buffered, nil
}

// ValidatePolygon cleans a polygon using Buffer(0) to resolve precision artifacts.
// This rebuilds topology and eliminates edge cases that cause "side location conflict".
func ValidatePolygon(g geom.Geometry) (geom.Geometry, error) {
	if g.IsEmpty() {
		return g, nil
	}
	// Buffer(0) silently returns an EMPTY geometry for non-areal input
	// (LineString/Point, or a GeometryCollection with no 2-D part), which a
	// caller cannot distinguish from "a real polygon that cleaned away to
	// nothing". Reject non-areal input with a distinct error instead. The
	// drop-path callers (buildProjectedMultiPolygon / buildProjectedGeometry-
	// Collection) already treat an error the same as an empty result — a
	// dropped-and-counted part — so this is neutral there; it only turns a
	// silent surprise into a signal for callers that expect a polygon.
	if g.Dimension() < 2 {
		return geom.Geometry{}, fmt.Errorf("ValidatePolygon: non-areal input (dimension %d)", g.Dimension())
	}
	return geom.Buffer(g, 0)
}

// RetainPolygonal reduces g to its 2-D parts. LineString, Point, and
// lower-dimension members of GeometryCollections are dropped; remaining
// polygons are merged via UnaryUnion so the result is a clean polygonal
// geometry.
//
// JTS OverlayNG runs in default (non-strict) mode under simplefeatures, where
// Intersection of two polygons CAN return a mixed-dimension GeometryCollection
// (the 2-D overlap plus 1-D linestrings along boundary segments the operands
// share outside that overlap — very common for OSM water polygons whose edges
// follow city boundaries along a river/coast). RetainPolygonal strips those
// 1-D artifacts back to the 2-D overlap, which is what callers actually want.
//
// As of simplefeatures v0.59.0 the overlay ops (Difference/Union/UnionMany/...)
// accept a mixed-dimension GC operand without erroring, and Difference's own
// output is already clean — so this is a NORMALIZATION step, not crash
// avoidance. It is load-bearing where the result geometry is kept or exported:
// Union/UnionMany propagate the 1-D parts into their output (a GC with a stray
// LineString), so a clipped hex stored via clipHexToCandidates would otherwise
// serialize as a malformed GeometryCollection feature. (Originally added for
// solvent-streets-i3ih, which manifested as an "Overlay input is
// mixed-dimension" panic on the pre-v0.59.0 simplefeatures then in use.)
func RetainPolygonal(g geom.Geometry) (geom.Geometry, error) {
	if g.IsEmpty() {
		return g, nil
	}
	if g.IsPolygon() || g.IsMultiPolygon() {
		return g, nil
	}
	if !g.IsGeometryCollection() {
		return geom.Geometry{}, nil
	}
	gc, _ := g.AsGeometryCollection()
	var polys []geom.Geometry
	for i := range gc.NumGeometries() {
		child, err := RetainPolygonal(gc.GeometryN(i))
		if err != nil {
			return geom.Geometry{}, err
		}
		if !child.IsEmpty() {
			polys = append(polys, child)
		}
	}
	if len(polys) == 0 {
		return geom.Geometry{}, nil
	}
	if len(polys) == 1 {
		return polys[0], nil
	}
	return geom.UnaryUnion(geom.NewGeometryCollection(polys).AsGeometry())
}

// UnionAll computes the unary union of all geometries, removing overlaps.
func UnionAll(geometries []geom.Geometry) (geom.Geometry, error) {
	if len(geometries) == 0 {
		return geom.Geometry{}, errors.New("no geometries to union")
	}
	if len(geometries) == 1 {
		return geometries[0], nil
	}
	return geom.UnionMany(geometries)
}

// GeometryToGeoJSON converts a geometry to GeoJSON using the given
// projector at the default coordinate precision (~1cm). Prefer
// GeometryToGeoJSONWithPrecision in code paths that read precision from
// configuration.
func GeometryToGeoJSON(g geom.Geometry, proj *UTMProjector) (string, error) {
	return GeometryToGeoJSONWithPrecision(g, proj, 7)
}

// GeometryToGeoJSONWithPrecision converts a geometry to GeoJSON using
// the given projector, rounding lon/lat to `decimals` decimal places.
// Use the exported config.Config.CoordinateDecimals() to source the
// precision from pvmt.toml.
func GeometryToGeoJSONWithPrecision(g geom.Geometry, proj *UTMProjector, decimals int) (string, error) {
	raw, err := g.MarshalJSON()
	if err != nil {
		return "", fmt.Errorf("marshal geojson: %w", err)
	}

	var gjObj map[string]any
	if err := json.Unmarshal(raw, &gjObj); err != nil {
		return "", err
	}
	reprojectGeoJSON(gjObj, proj, decimals)
	result, err := json.Marshal(gjObj)
	if err != nil {
		return "", err
	}
	return string(result), nil
}

func reprojectGeoJSON(obj map[string]any, proj *UTMProjector, decimals int) {
	if coords, ok := obj["coordinates"]; ok {
		obj["coordinates"] = reprojectCoords(coords, proj, decimals)
	}
	if geoms, ok := obj["geometries"].([]any); ok {
		for _, g := range geoms {
			if gm, ok := g.(map[string]any); ok {
				reprojectGeoJSON(gm, proj, decimals)
			}
		}
	}
}

// tryReprojectCoord checks if c is a coordinate pair [lon, lat] (both float64)
// and reprojects it if it is not already in lon/lat range. Returns the
// reprojected slice and true if c was a coordinate pair, or nil and false
// otherwise.
func tryReprojectCoord(c []any, proj *UTMProjector, decimals int) ([]any, bool) {
	if len(c) < 2 {
		return nil, false
	}
	x, ok := c[0].(float64)
	if !ok {
		return nil, false
	}
	y, ok := c[1].(float64)
	if !ok {
		return nil, false
	}
	if !isLonLat(x, y) {
		lon, lat := proj.FromProjected(x, y)
		return []any{roundTo(lon, decimals), roundTo(lat, decimals)}, true
	}
	return c, true
}

func reprojectCoords(v any, proj *UTMProjector, decimals int) any {
	c, ok := v.([]any)
	if !ok {
		return v
	}
	if reprojected, isCoord := tryReprojectCoord(c, proj, decimals); isCoord {
		return reprojected
	}
	result := make([]any, len(c))
	for i, item := range c {
		result[i] = reprojectCoords(item, proj, decimals)
	}
	return result
}

// isLonLat reports whether (x, y) already looks like a WGS84 lon/lat pair
// rather than projected UTM meters. It is a VALUE-RANGE heuristic driving the
// GeoJSON reprojection walk (see tryReprojectCoord): a coordinate already
// inside [-180,180]×[-90,90] is assumed to be lon/lat and passed through
// untouched, and anything outside is assumed projected and sent through
// FromProjected.
//
// The heuristic is safe here ONLY because our projected space is UTM
// easting/northing in meters, whose magnitudes never fall inside that box:
// easting carries a 500 000 m false easting (and stays within ~167 000–833 000
// m of it), and northing is either ≥ ~1e6 m (northern hemisphere) or offset by
// a 10 000 000 m false northing (southern) — so a genuine UTM pair always has
// |x| > 180. Do NOT reuse this for coordinate systems whose values can
// legitimately land in [-180,180]×[-90,90] (e.g. a small local meter grid near
// the origin), because projected points there would be misclassified as
// lon/lat and skipped, silently emitting raw meters as if they were degrees.
func isLonLat(x, y float64) bool {
	return math.Abs(x) <= 180 && math.Abs(y) <= 90
}

func roundTo(v float64, decimals int) float64 {
	pow := math.Pow(10, float64(decimals))
	return math.Round(v*pow) / pow
}

func coordsToSequence(coords [][2]float64) geom.Sequence {
	flat := make([]float64, len(coords)*2)
	for i, c := range coords {
		flat[i*2] = c[0]
		flat[i*2+1] = c[1]
	}
	seq := geom.NewSequence(flat, geom.DimXY)
	return seq
}

// GeoJSONToProjectedGeometry converts a GeoJSON geometry to a
// simplefeatures Geometry using the given projector. Any sub-parts of a
// MultiPolygon or GeometryCollection that fail to build or clean are
// silently dropped; use GeoJSONToProjectedGeometryDropped when the caller
// needs to surface a warning about partial data loss.
func GeoJSONToProjectedGeometry(gjson string, proj *UTMProjector) (geom.Geometry, string, error) {
	g, gtype, _, err := GeoJSONToProjectedGeometryDropped(gjson, proj)
	return g, gtype, err
}

// GeoJSONToProjectedGeometryDropped is GeoJSONToProjectedGeometry that also
// reports how many sub-parts (MultiPolygon members or GeometryCollection
// children) were dropped because they failed to build or clean. A non-zero
// count means the returned geometry is missing landmasses present in the
// input — callers handling whole-city boundaries should warn so operators
// can fix the source data. The count is always 0 for single-part types
// (Polygon, LineString, MultiLineString).
func GeoJSONToProjectedGeometryDropped(gjson string, proj *UTMProjector) (geom.Geometry, string, int, error) {
	var obj struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(gjson), &obj); err != nil {
		return geom.Geometry{}, "", 0, fmt.Errorf("parse geojson: %w", err)
	}

	switch obj.Type {
	case "LineString":
		g, err := buildProjectedLineString(obj.Coordinates, proj)
		if err != nil {
			return geom.Geometry{}, "", 0, err
		}
		return g, obj.Type, 0, nil

	case "MultiLineString":
		g, err := buildProjectedMultiLineString(obj.Coordinates, proj)
		if err != nil {
			return geom.Geometry{}, obj.Type, 0, err
		}
		return g, obj.Type, 0, nil

	case "Polygon":
		g, err := buildProjectedPolygon(obj.Coordinates, proj)
		if err != nil {
			return geom.Geometry{}, "", 0, err
		}
		return g, obj.Type, 0, nil

	case "MultiPolygon":
		g, dropped, err := buildProjectedMultiPolygon(obj.Coordinates, proj)
		if err != nil {
			return geom.Geometry{}, obj.Type, dropped, err
		}
		return g, obj.Type, dropped, nil

	case "GeometryCollection":
		g, dropped, err := buildProjectedGeometryCollection(gjson, proj)
		if err != nil {
			return geom.Geometry{}, obj.Type, dropped, err
		}
		return g, obj.Type, dropped, nil

	default:
		return geom.Geometry{}, obj.Type, 0, fmt.Errorf("unsupported geometry type: %s", obj.Type)
	}
}

func buildProjectedLineString(coordsRaw json.RawMessage, proj *UTMProjector) (geom.Geometry, error) {
	var coords [][2]float64
	if err := json.Unmarshal(coordsRaw, &coords); err != nil {
		return geom.Geometry{}, err
	}
	projected := projectCoords(coords, proj)
	seq := coordsToSequence(projected)
	ls := geom.NewLineString(seq)
	return ls.AsGeometry(), nil
}

// buildProjectedMultiLineString projects each part of a GeoJSON MultiLineString
// into its own LineString and returns a geom.MultiLineString. Parts are kept
// separate (NOT concatenated) — joining their coordinates would fabricate bridge
// segments between disjoint polylines. Callers buffer each part individually.
func buildProjectedMultiLineString(coordsRaw json.RawMessage, proj *UTMProjector) (geom.Geometry, error) {
	var lines [][][2]float64
	if err := json.Unmarshal(coordsRaw, &lines); err != nil {
		return geom.Geometry{}, err
	}
	var lss []geom.LineString
	for _, line := range lines {
		projected := projectCoords(line, proj)
		if len(projected) < 2 {
			continue
		}
		lss = append(lss, geom.NewLineString(coordsToSequence(projected)))
	}
	if len(lss) == 0 {
		return geom.Geometry{}, errors.New("no valid linestrings in MultiLineString")
	}
	return geom.NewMultiLineString(lss).AsGeometry(), nil
}

// buildProjectedMultiPolygon projects each member polygon, cleaning it with
// ValidatePolygon. Members that fail to build or clean are dropped; the
// second return value reports how many were dropped so callers can warn
// about partial data loss (a degenerate sub-polygon can otherwise delete a
// whole landmass undetected). Zero surviving members is an error.
func buildProjectedMultiPolygon(coordsRaw json.RawMessage, proj *UTMProjector) (geom.Geometry, int, error) {
	var polys [][][][2]float64
	if err := json.Unmarshal(coordsRaw, &polys); err != nil {
		return geom.Geometry{}, 0, err
	}
	var geometries []geom.Geometry
	dropped := 0
	for _, polyCoords := range polys {
		raw, _ := json.Marshal(polyCoords)
		g, err := buildProjectedPolygon(raw, proj)
		if err != nil {
			dropped++
			continue
		}
		cleaned, err := ValidatePolygon(g)
		if err != nil {
			dropped++
			continue
		}
		// Buffer(0) is robust enough that it rarely returns an error; a
		// degenerate sub-polygon (collapsed/zero-area ring) instead cleans
		// to an EMPTY geometry. Appending that would silently contribute
		// nothing to the union — count it as a drop too.
		if cleaned.IsEmpty() {
			dropped++
			continue
		}
		geometries = append(geometries, cleaned)
	}
	if len(geometries) == 0 {
		return geom.Geometry{}, dropped, errors.New("no valid polygons in MultiPolygon")
	}
	if len(geometries) == 1 {
		return geometries[0], dropped, nil
	}
	g, err := UnionAll(geometries)
	return g, dropped, err
}

// buildProjectedGeometryCollection mirrors buildProjectedMultiPolygon's
// drop-and-count behavior for GeometryCollection children.
func buildProjectedGeometryCollection(gjson string, proj *UTMProjector) (geom.Geometry, int, error) {
	var raw struct {
		Geometries []json.RawMessage `json:"geometries"`
	}
	if err := json.Unmarshal([]byte(gjson), &raw); err != nil {
		return geom.Geometry{}, 0, err
	}
	var geometries []geom.Geometry
	dropped := 0
	for _, gRaw := range raw.Geometries {
		// Count nested drops too: a child MultiPolygon that loses members
		// contributes those to this collection's dropped total.
		g, _, childDropped, err := GeoJSONToProjectedGeometryDropped(string(gRaw), proj)
		if err != nil {
			dropped++
			continue
		}
		dropped += childDropped
		cleaned, err := ValidatePolygon(g)
		if err != nil {
			dropped++
			continue
		}
		if cleaned.IsEmpty() {
			dropped++
			continue
		}
		geometries = append(geometries, cleaned)
	}
	if len(geometries) == 0 {
		return geom.Geometry{}, dropped, errors.New("no valid geometries in collection")
	}
	g, err := UnionAll(geometries)
	return g, dropped, err
}

func buildProjectedPolygon(coordsRaw json.RawMessage, proj *UTMProjector) (geom.Geometry, error) {
	var rings [][][2]float64
	if err := json.Unmarshal(coordsRaw, &rings); err != nil {
		return geom.Geometry{}, err
	}
	lineRings := make([]geom.LineString, len(rings))
	for i, ring := range rings {
		projected := projectCoords(ring, proj)
		seq := coordsToSequence(projected)
		lineRings[i] = geom.NewLineString(seq)
	}
	poly := geom.NewPolygon(lineRings)
	return poly.AsGeometry(), nil
}

func projectCoords(coords [][2]float64, proj *UTMProjector) [][2]float64 {
	projected := make([][2]float64, len(coords))
	for i, c := range coords {
		x, y := proj.ToProjected(c[0], c[1])
		projected[i] = [2]float64{x, y}
	}
	return projected
}
