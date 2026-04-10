package geo

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

const (
	geomTypePolygon      = "Polygon"
	geomTypeMultiPolygon = "MultiPolygon"
)

// bboxAccumulator tracks min/max coordinates while visiting GeoJSON.
type bboxAccumulator struct {
	minLon, minLat, maxLon, maxLat float64
}

func newBBoxAccumulator() *bboxAccumulator {
	return &bboxAccumulator{
		minLon: math.MaxFloat64,
		minLat: math.MaxFloat64,
		maxLon: -math.MaxFloat64,
		maxLat: -math.MaxFloat64,
	}
}

func (a *bboxAccumulator) addCoord(lon, lat float64) {
	if lon < a.minLon {
		a.minLon = lon
	}
	if lon > a.maxLon {
		a.maxLon = lon
	}
	if lat < a.minLat {
		a.minLat = lat
	}
	if lat > a.maxLat {
		a.maxLat = lat
	}
}

func (a *bboxAccumulator) validate() ([4]float64, error) {
	if a.minLon == math.MaxFloat64 {
		return [4]float64{}, errors.New("no coordinates found")
	}
	if a.minLat < -90 || a.maxLat > 90 {
		return [4]float64{}, fmt.Errorf("latitude out of range [-90, 90]: got [%f, %f]", a.minLat, a.maxLat)
	}
	if a.minLon < -180 || a.maxLon > 180 {
		return [4]float64{}, fmt.Errorf("longitude out of range [-180, 180]: got [%f, %f]", a.minLon, a.maxLon)
	}
	if a.minLat >= a.maxLat {
		return [4]float64{}, fmt.Errorf("south (%f) must be less than north (%f)", a.minLat, a.maxLat)
	}
	if a.minLon >= a.maxLon {
		return [4]float64{}, fmt.Errorf("west (%f) must be less than east (%f)", a.minLon, a.maxLon)
	}
	return [4]float64{a.minLat, a.minLon, a.maxLat, a.maxLon}, nil
}

// visitCoords recursively traverses nested JSON arrays, adding leaf coordinates
// to the accumulator. depth indicates the nesting level: 0 means v is a
// coordinate [lon, lat, ...].
func visitCoords(v json.RawMessage, depth int, acc *bboxAccumulator) error {
	if depth == 0 {
		var coord []float64
		if err := json.Unmarshal(v, &coord); err != nil {
			return err
		}
		if len(coord) < 2 {
			return errors.New("coordinate has fewer than 2 values")
		}
		acc.addCoord(coord[0], coord[1])
		return nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(v, &arr); err != nil {
		return err
	}
	for _, el := range arr {
		if err := visitCoords(el, depth-1, acc); err != nil {
			return err
		}
	}
	return nil
}

// BBoxFromGeoJSON extracts a bounding box [south, west, north, east] from a
// GeoJSON Polygon or MultiPolygon geometry.
func BBoxFromGeoJSON(geometryJSON string) ([4]float64, error) {
	var raw struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(geometryJSON), &raw); err != nil {
		return [4]float64{}, fmt.Errorf("parse geometry: %w", err)
	}

	acc := newBBoxAccumulator()

	// Polygon coordinates: [ring][point] → depth 2
	// MultiPolygon coordinates: [polygon][ring][point] → depth 3
	switch raw.Type {
	case geomTypePolygon:
		if err := visitCoords(raw.Coordinates, 2, acc); err != nil {
			return [4]float64{}, fmt.Errorf("parse polygon coordinates: %w", err)
		}
	case geomTypeMultiPolygon:
		if err := visitCoords(raw.Coordinates, 3, acc); err != nil {
			return [4]float64{}, fmt.Errorf("parse multipolygon coordinates: %w", err)
		}
	default:
		return [4]float64{}, fmt.Errorf("unsupported geometry type %q, want Polygon or MultiPolygon", raw.Type)
	}

	return acc.validate()
}

// CenterFromBBox returns the center (lon, lat) of a [south, west, north, east] bbox.
func CenterFromBBox(bbox [4]float64) (lon, lat float64) {
	lat = (bbox[0] + bbox[2]) / 2
	lon = (bbox[1] + bbox[3]) / 2
	return lon, lat
}
