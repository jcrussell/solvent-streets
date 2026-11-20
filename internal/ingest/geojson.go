package ingest

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
)

// coordDecimals is the precision ingest writes coordinates at: 7 decimal
// degrees, about a centimetre and the precision OSM stores.
const coordDecimals = 7

// errNonFiniteCoord is returned for a NaN or infinite coordinate, which
// GeoJSON cannot represent.
var errNonFiniteCoord = errors.New("geojson: non-finite coordinate")

// coord is one GeoJSON position, [lon,lat], written with exactly
// coordDecimals fixed decimals and never in exponent form. The geometry text
// ingest builds must be a pure function of the coordinates: a stored
// boundary keeps this text verbatim and its input digest hashes it, so any
// change in how a coordinate prints re-stales every city's compute.
// encoding/json's own float format (shortest round-trip digits, exponent
// form below 1e-6) has no fixed-decimal option, hence this MarshalJSON.
type coord [2]float64

// MarshalJSON implements json.Marshaler.
func (c coord) MarshalJSON() ([]byte, error) {
	for _, v := range c {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, errNonFiniteCoord
		}
	}
	b := make([]byte, 0, 2*(coordDecimals+6))
	b = append(b, '[')
	b = strconv.AppendFloat(b, c[0], 'f', coordDecimals, 64)
	b = append(b, ',')
	b = strconv.AppendFloat(b, c[1], 'f', coordDecimals, 64)
	return append(b, ']'), nil
}

// geoJSONGeometry is a GeoJSON geometry object. C is the coordinates'
// nesting: coord for a Point, []coord for a LineString, and so on.
type geoJSONGeometry[C any] struct {
	Type        string `json:"type"`
	Coordinates C      `json:"coordinates"`
}

// marshalGeometry is the GeoJSON text of a geometry of type typ.
func marshalGeometry[C any](typ string, coordinates C) (string, error) {
	b, err := json.Marshal(geoJSONGeometry[C]{Type: typ, Coordinates: coordinates})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// coordList is a line or ring as GeoJSON positions. It is never nil, so an
// empty list marshals as [] rather than null.
func coordList(coords [][2]float64) []coord {
	out := make([]coord, len(coords))
	for i, c := range coords {
		out[i] = c
	}
	return out
}

func coordToPointGeoJSON(c [2]float64) (string, error) {
	return marshalGeometry(geomPoint, coord(c))
}

func coordsToLineStringGeoJSON(coords [][2]float64) (string, error) {
	return marshalGeometry(geomLineString, coordList(coords))
}

func coordsToPolygonGeoJSON(coords [][2]float64) (string, error) {
	return marshalGeometry(geomPolygon, [][]coord{coordList(coords)})
}

// polysToMultiPolygonGeoJSON is one MultiPolygon of polys, each its outer
// ring followed by its holes.
func polysToMultiPolygonGeoJSON(polys []waterPolygon) (string, error) {
	out := make([][][]coord, len(polys))
	for i, p := range polys {
		rings := make([][]coord, 0, 1+len(p.holes))
		rings = append(rings, coordList(p.outer))
		for _, h := range p.holes {
			rings = append(rings, coordList(h))
		}
		out[i] = rings
	}
	return marshalGeometry(geomMultiPolygon, out)
}
