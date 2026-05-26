package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"

	"github.com/jcrussell/solvent-streets/internal/logs"
)

// Sentinels for FetchCityBoundaryFromRelation failure modes. Callers
// (resolveBoundary in pkg/cmd/ingest) detect these via errors.Is and
// attach remediation hints via cmdutil.Hintf.
var (
	// ErrBoundaryRelationNotFound signals that the requested Overpass
	// relation either isn't in the response (deleted/wrong ID) or has
	// no closed outer rings after stitching (broken multipolygon).
	ErrBoundaryRelationNotFound = errors.New("overpass: boundary relation not found or has no closed outer rings")
	// ErrBoundaryRelationTooLarge signals a bbox span > 5° in either
	// dimension — the operator almost certainly pasted a county or
	// state relation id instead of a city's. Cheap footgun gate.
	ErrBoundaryRelationTooLarge = errors.New("overpass: boundary relation spans more than 5° (likely the wrong ID — a county or state)")
)

// boundaryRelationMaxSpanDeg caps the lon/lat span of an accepted
// boundary at 5°. Real city boundaries are sub-degree; counties are
// ~1-2°; US states cross several degrees. Catches the common typo of
// passing a parent admin relation's id.
const boundaryRelationMaxSpanDeg = 5.0

// FetchCityBoundaryFromRelation fetches an OSM admin boundary relation
// by ID via Overpass and returns its (Multi)Polygon as a GeoJSON
// string. Use this when Nominatim's search-by-name path fails to
// surface the boundary — e.g. cities tagged place=city on a node
// rather than as a boundary=administrative relation. Bounded to
// city-scale relations (admin_level=8 is the design target);
// stitchRings is O(n²) on member count so larger relations are
// allowed but slower.
func FetchCityBoundaryFromRelation(ctx context.Context, client *http.Client, relationID int64) (string, error) {
	return fetchCityBoundaryFromRelation(ctx, client, overpassAPI, relationID)
}

func fetchCityBoundaryFromRelation(ctx context.Context, client *http.Client, baseURL string, relationID int64) (string, error) {
	if relationID <= 0 {
		return "", fmt.Errorf("invalid relation id %d", relationID)
	}

	query := fmt.Sprintf("[out:json][timeout:60];relation(%d);out geom;", relationID)
	body, err := postOverpass(ctx, client, baseURL, query)
	if err != nil {
		return "", err
	}

	var resp overpassResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parse overpass response: %w", err)
	}

	var rel *overpassElement
	for i := range resp.Elements {
		if resp.Elements[i].Type == elementRelation && resp.Elements[i].ID == relationID {
			rel = &resp.Elements[i]
			break
		}
	}
	if rel == nil {
		return "", fmt.Errorf("%w: id=%d", ErrBoundaryRelationNotFound, relationID)
	}

	// relationToPolygons reuses the same stitching pipeline as water
	// (water.go), minus the water-specific acceptWaterPolygon filter
	// — admin boundaries can legitimately cover the full query bbox.
	// The returned waterPolygon type is a shared shape (outer +
	// holes); the name is a historical artifact, not water-specific.
	polys := relationToPolygons(ctx, *rel)
	if len(polys) == 0 {
		return "", fmt.Errorf("%w: id=%d", ErrBoundaryRelationNotFound, relationID)
	}

	if span := outerBBoxSpanDeg(polys); span > boundaryRelationMaxSpanDeg {
		return "", fmt.Errorf("%w: id=%d span=%.2f°", ErrBoundaryRelationTooLarge, relationID, span)
	}

	logs.From(ctx).Info("boundary relation fetched",
		"relation", relationID, "polygons", len(polys),
	)
	return polysToMultiPolygonGeoJSON(polys), nil
}

// outerBBoxSpanDeg returns the larger of the lon/lat dimensions of
// the bbox enclosing all outer rings, in degrees. Used as the input
// to the boundaryRelationMaxSpanDeg footgun gate.
func outerBBoxSpanDeg(polys []waterPolygon) float64 {
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, p := range polys {
		for _, c := range p.outer {
			if c[0] < minX {
				minX = c[0]
			}
			if c[0] > maxX {
				maxX = c[0]
			}
			if c[1] < minY {
				minY = c[1]
			}
			if c[1] > maxY {
				maxY = c[1]
			}
		}
	}
	if math.IsInf(minX, 1) {
		return 0
	}
	return math.Max(maxX-minX, maxY-minY)
}
