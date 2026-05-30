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

// boundaryCloseTolDeg snaps a near-closed outer/inner ring shut when its
// two ends fall within this lon/lat distance (~100 m at mid-latitudes).
// OSM admin boundaries routinely have a sub-100 m gap where two adjacent
// member ways don't share an exact node (e.g. Denver, relation 1411339,
// whose ~10 m closure gap otherwise drops the entire ring). Real boundary
// openings are far larger, and tolerant closure never merges distinct
// chains, so this only recovers genuine near-misses.
const boundaryCloseTolDeg = 1e-3

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

	// Span-gate the raw member geometry BEFORE stitching: stitchRings is
	// O(n²) on member count, so an operator who pastes a county or state
	// relation id otherwise pays the full stitching cost before the
	// post-stitch gate trips. The pre-check sees the entire member-way
	// envelope (including unstitchable fragments) while the post-stitch
	// gate only sees accepted outer rings, so this check is a strict
	// superset on the DoS path. The post-stitch outerBBoxSpanDeg call
	// below is retained as the source-of-truth gate on the final
	// accepted geometry.
	if span := memberBBoxSpanDeg(rel.Members); span > boundaryRelationMaxSpanDeg {
		return "", fmt.Errorf("%w: id=%d span=%.2f°", ErrBoundaryRelationTooLarge, relationID, span)
	}

	// relationToPolygons reuses the same stitching pipeline as water
	// (water.go), minus the water-specific acceptWaterPolygon filter
	// — admin boundaries can legitimately cover the full query bbox.
	// The returned waterPolygon type is a shared shape (outer +
	// holes); the name is a historical artifact, not water-specific.
	// Admin boundaries use the bidirectional stitcher with tolerant closure:
	// member ways are often inconsistently oriented and have sub-~100 m
	// closure gaps (e.g. Denver, relation 1411339).
	polys := relationToPolygons(ctx, *rel, stitchRingsBidi, nil)
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

// memberBBoxSpanDeg returns the larger of the lon/lat dimensions of
// the bbox enclosing every way member's raw geometry, in degrees. It
// is the pre-stitch counterpart to outerBBoxSpanDeg: cheap to compute
// (one pass over members) and operates on the unstitched coordinate
// stream, so it can short-circuit the O(n²) stitchRings call when the
// operator pasted a parent admin relation. Returns 0 for relations
// with no way members or no geometry — those cases are handled
// downstream by the stitching pipeline.
func memberBBoxSpanDeg(members []overpassRelationMember) float64 {
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, m := range members {
		if m.Type != elementWay {
			continue
		}
		for _, g := range m.Geometry {
			if g.Lon < minX {
				minX = g.Lon
			}
			if g.Lon > maxX {
				maxX = g.Lon
			}
			if g.Lat < minY {
				minY = g.Lat
			}
			if g.Lat > maxY {
				maxY = g.Lat
			}
		}
	}
	if math.IsInf(minX, 1) {
		return 0
	}
	return math.Max(maxX-minX, maxY-minY)
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
