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
	return geom.Buffer(g, 0)
}

// RetainPolygonal reduces g to its 2-D parts. LineString, Point, and
// lower-dimension members of GeometryCollections are dropped; remaining
// polygons are merged via UnaryUnion so the result is safe to hand to
// geom.Intersection / geom.Difference.
//
// The JTS port underneath simplefeatures panics with "Overlay input is
// mixed-dimension" on a GeometryCollection containing geometries of
// differing dimensions, and simplefeatures' self-defense for GC operands
// (alg_overlay.go::prepareOverlayInputParts) routes through UnaryUnion,
// which inherits the same restriction. Critically, the documented JTS
// OverlayNG default-mode behavior is that Intersection of two polygons
// CAN return a mixed-dim GC when the polygons share boundary segments
// outside their 2-D overlap (very common for OSM water polygons whose
// edges follow city boundaries along a river/coast). Feeding that GC
// directly into Difference triggers the panic; calling RetainPolygonal
// in between filters out the 1-D shared-edge artifacts and leaves only
// the 2-D overlap, which is what "subtract water area" wants anyway.
// Closes solvent-streets-i3ih.
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
		lon, lat, _ := proj.FromProjected(x, y)
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

// ParseGeoJSONCoords extracts coordinate arrays from a GeoJSON geometry string.
// Supported types: LineString, Polygon, MultiLineString.
// Limitations:
//   - Polygon: only the exterior ring (index 0) is returned; interior rings
//     (holes/islands) are discarded. This is acceptable for road/sidewalk width
//     buffering where polygons represent simple surface areas.
//   - MultiPolygon: not supported (returns an error). Use GeoJSONToProjectedGeometry
//     for full MultiPolygon handling.
func ParseGeoJSONCoords(gjson string) ([][2]float64, string, error) {
	var obj struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(gjson), &obj); err != nil {
		return nil, "", fmt.Errorf("parse geojson: %w", err)
	}

	switch obj.Type {
	case "LineString":
		var coords [][2]float64
		if err := json.Unmarshal(obj.Coordinates, &coords); err != nil {
			return nil, "", err
		}
		return coords, obj.Type, nil
	case "Polygon":
		var rings [][][2]float64
		if err := json.Unmarshal(obj.Coordinates, &rings); err != nil {
			return nil, "", err
		}
		if len(rings) > 0 {
			return rings[0], obj.Type, nil
		}
		return nil, obj.Type, nil
	case "MultiLineString":
		var lines [][][2]float64
		if err := json.Unmarshal(obj.Coordinates, &lines); err != nil {
			return nil, "", err
		}
		var all [][2]float64
		for _, line := range lines {
			all = append(all, line...)
		}
		return all, obj.Type, nil
	default:
		return nil, obj.Type, fmt.Errorf("unsupported geometry type: %s", obj.Type)
	}
}

// GeoJSONToProjectedGeometry converts a GeoJSON geometry to a
// simplefeatures Geometry using the given projector.
func GeoJSONToProjectedGeometry(gjson string, proj *UTMProjector) (geom.Geometry, string, error) {
	var obj struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(gjson), &obj); err != nil {
		return geom.Geometry{}, "", fmt.Errorf("parse geojson: %w", err)
	}

	switch obj.Type {
	case "LineString":
		g, err := buildProjectedLineString(obj.Coordinates, proj)
		if err != nil {
			return geom.Geometry{}, "", err
		}
		return g, obj.Type, nil

	case "MultiLineString":
		g, err := buildProjectedMultiLineString(obj.Coordinates, proj)
		if err != nil {
			return geom.Geometry{}, obj.Type, err
		}
		return g, obj.Type, nil

	case "Polygon":
		g, err := buildProjectedPolygon(obj.Coordinates, proj)
		if err != nil {
			return geom.Geometry{}, "", err
		}
		return g, obj.Type, nil

	case "MultiPolygon":
		g, err := buildProjectedMultiPolygon(obj.Coordinates, proj)
		if err != nil {
			return geom.Geometry{}, obj.Type, err
		}
		return g, obj.Type, nil

	case "GeometryCollection":
		g, err := buildProjectedGeometryCollection(gjson, proj)
		if err != nil {
			return geom.Geometry{}, obj.Type, err
		}
		return g, obj.Type, nil

	default:
		return geom.Geometry{}, obj.Type, fmt.Errorf("unsupported geometry type: %s", obj.Type)
	}
}

func buildProjectedLineString(coordsRaw json.RawMessage, proj *UTMProjector) (geom.Geometry, error) {
	var coords [][2]float64
	if err := json.Unmarshal(coordsRaw, &coords); err != nil {
		return geom.Geometry{}, err
	}
	projected, err := projectCoords(coords, proj)
	if err != nil {
		return geom.Geometry{}, err
	}
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
		projected, err := projectCoords(line, proj)
		if err != nil {
			continue
		}
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

func buildProjectedMultiPolygon(coordsRaw json.RawMessage, proj *UTMProjector) (geom.Geometry, error) {
	var polys [][][][2]float64
	if err := json.Unmarshal(coordsRaw, &polys); err != nil {
		return geom.Geometry{}, err
	}
	var geometries []geom.Geometry
	for _, polyCoords := range polys {
		raw, _ := json.Marshal(polyCoords)
		g, err := buildProjectedPolygon(raw, proj)
		if err != nil {
			continue
		}
		cleaned, err := ValidatePolygon(g)
		if err != nil {
			continue
		}
		geometries = append(geometries, cleaned)
	}
	if len(geometries) == 0 {
		return geom.Geometry{}, errors.New("no valid polygons in MultiPolygon")
	}
	if len(geometries) == 1 {
		return geometries[0], nil
	}
	return UnionAll(geometries)
}

func buildProjectedGeometryCollection(gjson string, proj *UTMProjector) (geom.Geometry, error) {
	var raw struct {
		Geometries []json.RawMessage `json:"geometries"`
	}
	if err := json.Unmarshal([]byte(gjson), &raw); err != nil {
		return geom.Geometry{}, err
	}
	var geometries []geom.Geometry
	for _, gRaw := range raw.Geometries {
		g, _, err := GeoJSONToProjectedGeometry(string(gRaw), proj)
		if err != nil {
			continue
		}
		cleaned, err := ValidatePolygon(g)
		if err != nil {
			continue
		}
		geometries = append(geometries, cleaned)
	}
	if len(geometries) == 0 {
		return geom.Geometry{}, errors.New("no valid geometries in collection")
	}
	return UnionAll(geometries)
}

func buildProjectedPolygon(coordsRaw json.RawMessage, proj *UTMProjector) (geom.Geometry, error) {
	var rings [][][2]float64
	if err := json.Unmarshal(coordsRaw, &rings); err != nil {
		return geom.Geometry{}, err
	}
	lineRings := make([]geom.LineString, len(rings))
	for i, ring := range rings {
		projected, err := projectCoords(ring, proj)
		if err != nil {
			return geom.Geometry{}, err
		}
		seq := coordsToSequence(projected)
		lineRings[i] = geom.NewLineString(seq)
	}
	poly := geom.NewPolygon(lineRings)
	return poly.AsGeometry(), nil
}

func projectCoords(coords [][2]float64, proj *UTMProjector) ([][2]float64, error) {
	projected := make([][2]float64, len(coords))
	for i, c := range coords {
		x, y, err := proj.ToProjected(c[0], c[1])
		if err != nil {
			return nil, fmt.Errorf("project coordinate %d: %w", i, err)
		}
		projected[i] = [2]float64{x, y}
	}
	return projected, nil
}
