package geo

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/peterstace/simplefeatures/geom"
)

// US survey foot
const usSurveyFoot = 1200.0 / 3937.0

// BufferLineString buffers a projected linestring by width/2 with flat end caps.
// Coordinates must already be in the projected coordinate system.
func BufferLineString(coords [][2]float64, widthProjected float64) (geom.Geometry, error) {
	if len(coords) < 2 {
		return geom.Geometry{}, fmt.Errorf("need at least 2 coordinates")
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

// UnionAll computes the unary union of all geometries, removing overlaps.
func UnionAll(geometries []geom.Geometry) (geom.Geometry, error) {
	if len(geometries) == 0 {
		return geom.Geometry{}, fmt.Errorf("no geometries to union")
	}
	if len(geometries) == 1 {
		return geometries[0], nil
	}

	return geom.UnionMany(geometries)
}

// AreaInProjectedUnits returns the raw area in the projector's coordinate units squared.
func AreaInProjectedUnits(g geom.Geometry) float64 {
	return g.Area()
}

// AreaAcres converts square feet to acres.
func AreaAcres(sqft float64) float64 {
	return sqft / 43560.0
}

// AreaSqFtFromProjected converts area from projected units to square feet.
// For UTM (meters), converts sq meters to sq feet. For Lambert (feet), returns as-is.
func AreaSqFtFromProjected(area float64, proj Projector) float64 {
	if proj.Unit() == "meters" {
		// 1 meter = 3.28084 feet, 1 sq meter = 10.7639 sq feet
		return area * 10.763910417
	}
	return area // already in sq feet
}

// GeometryToGeoJSON converts a geometry to GeoJSON using the given projector.
func GeometryToGeoJSON(g geom.Geometry, proj Projector) (string, error) {
	raw, err := g.MarshalJSON()
	if err != nil {
		return "", fmt.Errorf("marshal geojson: %w", err)
	}

	var gjObj map[string]any
	if err := json.Unmarshal(raw, &gjObj); err != nil {
		return "", err
	}
	reprojectGeoJSON(gjObj, proj)
	result, err := json.Marshal(gjObj)
	if err != nil {
		return "", err
	}
	return string(result), nil
}

func reprojectGeoJSON(obj map[string]any, proj Projector) {
	if coords, ok := obj["coordinates"]; ok {
		obj["coordinates"] = reprojectCoords(coords, proj)
	}
	if geoms, ok := obj["geometries"].([]any); ok {
		for _, g := range geoms {
			if gm, ok := g.(map[string]any); ok {
				reprojectGeoJSON(gm, proj)
			}
		}
	}
}

func reprojectCoords(v any, proj Projector) any {
	switch c := v.(type) {
	case []any:
		if len(c) >= 2 {
			if x, ok := c[0].(float64); ok {
				if y, ok := c[1].(float64); ok {
					if !isLonLat(x, y) {
						lon, lat, _ := proj.FromProjected(x, y)
						return []any{roundTo(lon, 7), roundTo(lat, 7)}
					}
					return c
				}
			}
		}
		result := make([]any, len(c))
		for i, item := range c {
			result[i] = reprojectCoords(item, proj)
		}
		return result
	}
	return v
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
func GeoJSONToProjectedGeometry(gjson string, proj Projector) (geom.Geometry, string, error) {
	var obj struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(gjson), &obj); err != nil {
		return geom.Geometry{}, "", fmt.Errorf("parse geojson: %w", err)
	}

	switch obj.Type {
	case "LineString":
		var coords [][2]float64
		if err := json.Unmarshal(obj.Coordinates, &coords); err != nil {
			return geom.Geometry{}, "", err
		}
		projected, err := projectCoords(coords, proj)
		if err != nil {
			return geom.Geometry{}, "", err
		}
		seq := coordsToSequence(projected)
		ls := geom.NewLineString(seq)
		return ls.AsGeometry(), obj.Type, nil

	case "Polygon":
		g, err := buildProjectedPolygon(obj.Coordinates, proj)
		if err != nil {
			return geom.Geometry{}, "", err
		}
		return g, obj.Type, nil

	case "MultiPolygon":
		var polys [][][][2]float64
		if err := json.Unmarshal(obj.Coordinates, &polys); err != nil {
			return geom.Geometry{}, "", err
		}
		var geometries []geom.Geometry
		for _, polyCoords := range polys {
			raw, _ := json.Marshal(polyCoords)
			g, err := buildProjectedPolygon(raw, proj)
			if err != nil {
				continue
			}
			geometries = append(geometries, g)
		}
		if len(geometries) == 0 {
			return geom.Geometry{}, obj.Type, fmt.Errorf("no valid polygons in MultiPolygon")
		}
		if len(geometries) == 1 {
			return geometries[0], obj.Type, nil
		}
		union, err := UnionAll(geometries)
		if err != nil {
			return geom.Geometry{}, obj.Type, err
		}
		return union, obj.Type, nil

	case "GeometryCollection":
		var raw struct {
			Geometries []json.RawMessage `json:"geometries"`
		}
		if err := json.Unmarshal([]byte(gjson), &raw); err != nil {
			return geom.Geometry{}, obj.Type, err
		}
		var geometries []geom.Geometry
		for _, gRaw := range raw.Geometries {
			g, _, err := GeoJSONToProjectedGeometry(string(gRaw), proj)
			if err != nil {
				continue
			}
			geometries = append(geometries, g)
		}
		if len(geometries) == 0 {
			return geom.Geometry{}, obj.Type, fmt.Errorf("no valid geometries in collection")
		}
		union, err := UnionAll(geometries)
		if err != nil {
			return geom.Geometry{}, obj.Type, err
		}
		return union, obj.Type, nil

	default:
		return geom.Geometry{}, obj.Type, fmt.Errorf("unsupported geometry type: %s", obj.Type)
	}
}

func buildProjectedPolygon(coordsRaw json.RawMessage, proj Projector) (geom.Geometry, error) {
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

func projectCoords(coords [][2]float64, proj Projector) ([][2]float64, error) {
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

// WidthInProjectedUnits converts a width in meters to the projector's units.
func WidthInProjectedUnits(widthMeters float64, proj Projector) float64 {
	if proj.Unit() == "feet" {
		return widthMeters / usSurveyFoot
	}
	return widthMeters // already in meters for UTM
}
