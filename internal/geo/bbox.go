package geo

import (
	"encoding/json"
	"fmt"
	"math"
)

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

	minLon, minLat := math.MaxFloat64, math.MaxFloat64
	maxLon, maxLat := -math.MaxFloat64, -math.MaxFloat64

	var visit func(v json.RawMessage, depth int) error
	visit = func(v json.RawMessage, depth int) error {
		// At depth 0 we have a coordinate [lon, lat, ...]
		if depth == 0 {
			var coord []float64
			if err := json.Unmarshal(v, &coord); err != nil {
				return err
			}
			if len(coord) < 2 {
				return fmt.Errorf("coordinate has fewer than 2 values")
			}
			lon, lat := coord[0], coord[1]
			if lon < minLon {
				minLon = lon
			}
			if lon > maxLon {
				maxLon = lon
			}
			if lat < minLat {
				minLat = lat
			}
			if lat > maxLat {
				maxLat = lat
			}
			return nil
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(v, &arr); err != nil {
			return err
		}
		for _, el := range arr {
			if err := visit(el, depth-1); err != nil {
				return err
			}
		}
		return nil
	}

	// Polygon coordinates: [ring][point] → depth 2
	// MultiPolygon coordinates: [polygon][ring][point] → depth 3
	switch raw.Type {
	case "Polygon":
		if err := visit(raw.Coordinates, 2); err != nil {
			return [4]float64{}, fmt.Errorf("parse polygon coordinates: %w", err)
		}
	case "MultiPolygon":
		if err := visit(raw.Coordinates, 3); err != nil {
			return [4]float64{}, fmt.Errorf("parse multipolygon coordinates: %w", err)
		}
	default:
		return [4]float64{}, fmt.Errorf("unsupported geometry type %q, want Polygon or MultiPolygon", raw.Type)
	}

	if minLon == math.MaxFloat64 {
		return [4]float64{}, fmt.Errorf("no coordinates found")
	}

	// Validate coordinate ranges
	if minLat < -90 || maxLat > 90 {
		return [4]float64{}, fmt.Errorf("latitude out of range [-90, 90]: got [%f, %f]", minLat, maxLat)
	}
	if minLon < -180 || maxLon > 180 {
		return [4]float64{}, fmt.Errorf("longitude out of range [-180, 180]: got [%f, %f]", minLon, maxLon)
	}
	if minLat >= maxLat {
		return [4]float64{}, fmt.Errorf("south (%f) must be less than north (%f)", minLat, maxLat)
	}
	if minLon >= maxLon {
		return [4]float64{}, fmt.Errorf("west (%f) must be less than east (%f)", minLon, maxLon)
	}

	return [4]float64{minLat, minLon, maxLat, maxLon}, nil
}

// CenterFromBBox returns the center (lon, lat) of a [south, west, north, east] bbox.
func CenterFromBBox(bbox [4]float64) (lon, lat float64) {
	lat = (bbox[0] + bbox[2]) / 2
	lon = (bbox[1] + bbox[3]) / 2
	return lon, lat
}
