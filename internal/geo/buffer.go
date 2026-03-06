package geo

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/peterstace/simplefeatures/geom"
)

// BufferLineString buffers a projected linestring (in US survey feet) by width/2 with flat end caps.
func BufferLineString(coords [][2]float64, widthFeet float64) (geom.Geometry, error) {
	if len(coords) < 2 {
		return geom.Geometry{}, fmt.Errorf("need at least 2 coordinates")
	}
	seq := coordsToSequence(coords)
	ls := geom.NewLineString(seq)
	g := ls.AsGeometry()
	buffered, err := geom.Buffer(g, widthFeet/2, geom.BufferEndCapFlat(), geom.BufferQuadSegments(8))
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

// AreaSqFt returns the area of a geometry in square US survey feet.
func AreaSqFt(g geom.Geometry) float64 {
	return g.Area()
}

// AreaAcres converts square feet to acres.
func AreaAcres(sqft float64) float64 {
	return sqft / 43560.0
}

// GeometryToGeoJSON converts a simplefeatures geometry to a GeoJSON string,
// reprojecting from EPSG:2227 to WGS84.
func GeometryToGeoJSON(g geom.Geometry) (string, error) {
	gj := g.MarshalJSON
	raw, err := gj()
	if err != nil {
		return "", fmt.Errorf("marshal geojson: %w", err)
	}

	// Reproject all coordinates from state plane to WGS84
	var gjObj map[string]any
	if err := json.Unmarshal(raw, &gjObj); err != nil {
		return "", err
	}
	reprojectGeoJSON(gjObj)
	result, err := json.Marshal(gjObj)
	if err != nil {
		return "", err
	}
	return string(result), nil
}

func reprojectGeoJSON(obj map[string]any) {
	if coords, ok := obj["coordinates"]; ok {
		obj["coordinates"] = reprojectCoords(coords)
	}
	if geoms, ok := obj["geometries"].([]any); ok {
		for _, g := range geoms {
			if gm, ok := g.(map[string]any); ok {
				reprojectGeoJSON(gm)
			}
		}
	}
}

func reprojectCoords(v any) any {
	switch c := v.(type) {
	case []any:
		if len(c) >= 2 {
			if x, ok := c[0].(float64); ok {
				if y, ok := c[1].(float64); ok {
					if !isLonLat(x, y) {
						lon, lat := ToWGS84(x, y)
						return []any{roundTo(lon, 7), roundTo(lat, 7)}
					}
					return c
				}
			}
		}
		result := make([]any, len(c))
		for i, item := range c {
			result[i] = reprojectCoords(item)
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

// GeoJSONToProjectedGeometry converts a GeoJSON geometry to a simplefeatures Geometry in EPSG:2227.
func GeoJSONToProjectedGeometry(gjson string) (geom.Geometry, string, error) {
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
		projected := projectCoords(coords)
		seq := coordsToSequence(projected)
		ls := geom.NewLineString(seq)
		return ls.AsGeometry(), obj.Type, nil

	case "Polygon":
		var rings [][][2]float64
		if err := json.Unmarshal(obj.Coordinates, &rings); err != nil {
			return geom.Geometry{}, "", err
		}
		lineRings := make([]geom.LineString, len(rings))
		for i, ring := range rings {
			projected := projectCoords(ring)
			seq := coordsToSequence(projected)
			lineRings[i] = geom.NewLineString(seq)
		}
		poly := geom.NewPolygon(lineRings)
		return poly.AsGeometry(), obj.Type, nil

	default:
		return geom.Geometry{}, obj.Type, fmt.Errorf("unsupported geometry type: %s", obj.Type)
	}
}

func projectCoords(coords [][2]float64) [][2]float64 {
	projected := make([][2]float64, len(coords))
	for i, c := range coords {
		x, y := ToStatePlane(c[0], c[1])
		projected[i] = [2]float64{x, y}
	}
	return projected
}
