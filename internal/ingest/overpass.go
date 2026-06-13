package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

const overpassAPI = "https://overpass-api.de/api/interpreter" //nolint:gosec // G101: not a credential

const (
	geomLineString   = "LineString"
	geomPolygon      = "Polygon"
	geomMultiPolygon = "MultiPolygon"
)

const (
	elementWay      = "way"
	elementRelation = "relation"
)

type OverpassSource struct {
	BBox [4]float64 // [south, west, north, east]
}

var _ Source = (*OverpassSource)(nil)

func (s *OverpassSource) Name() string { return "overpass" }

const maxSplitDepth = 3 // max 4^3 = 64 requests per city/resource

func (s *OverpassSource) Fetch(ctx context.Context, client *http.Client, rt resource.Source) ([]db.Feature, error) {
	seen := make(map[string]bool)
	return fetchRecursive(ctx, client, rt, s.BBox, seen, 0)
}

func fetchRecursive(ctx context.Context, client *http.Client, rt resource.Source, bbox [4]float64, seen map[string]bool, depth int) ([]db.Feature, error) {
	features, err := fetchBBox(ctx, client, rt, bbox)
	if err != nil && isParseError(err) && depth < maxSplitDepth {
		// Response too large / truncated — split into quadrants and retry
		var all []db.Feature
		for _, q := range splitBBox(bbox) {
			qFeatures, qErr := fetchRecursive(ctx, client, rt, q, seen, depth+1)
			if qErr != nil {
				return nil, qErr
			}
			all = append(all, qFeatures...)
		}
		return all, nil
	}
	if err != nil {
		return nil, err
	}

	// Deduplicate features at quadrant boundaries
	var unique []db.Feature
	for _, f := range features {
		if !seen[f.ID] {
			seen[f.ID] = true
			unique = append(unique, f)
		}
	}
	return unique, nil
}

func fetchBBox(ctx context.Context, client *http.Client, rt resource.Source, bbox [4]float64) ([]db.Feature, error) {
	query := rt.OverpassQuery(bbox)

	// Overpass uses POST only because the query body is too large for a
	// URL; the request is semantically a read. Opt the ctx into retry so
	// the byob-http-client.3 transport will back off and re-issue on 429
	// / 502 / 503 / 504 instead of treating POST as a write that must
	// never replay.
	req, err := http.NewRequestWithContext(AllowRetry(ctx), http.MethodPost, overpassAPI, strings.NewReader(url.Values{"data": {query}}.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create overpass request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("overpass request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("read overpass response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("overpass returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	return parseOverpassResponse(ctx, body, rt.Type())
}

// errOverpassTruncated marks a 200 response whose top-level "remark" reports a
// server-side runtime failure (timeout / out of memory). Overpass returns valid
// JSON with a partial (often empty) elements array in that case, so it parses
// cleanly; we surface it as a parse-class error so fetchRecursive splits the
// bbox and retries with smaller queries.
var errOverpassTruncated = errors.New("overpass result truncated by server remark")

func isParseError(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "parse overpass json") ||
		errors.Is(err, errOverpassTruncated))
}

// remarkIndicatesTruncation reports whether an Overpass top-level remark signals
// a truncated result (runtime timeout / OOM) rather than a benign note, so a
// harmless informational remark does not trigger needless quadrant retries.
func remarkIndicatesTruncation(remark string) bool {
	r := strings.ToLower(remark)
	return strings.Contains(r, "runtime error") ||
		strings.Contains(r, "timed out") ||
		strings.Contains(r, "out of memory")
}

func splitBBox(bbox [4]float64) [4][4]float64 {
	midLat := (bbox[0] + bbox[2]) / 2
	midLon := (bbox[1] + bbox[3]) / 2
	return [4][4]float64{
		{bbox[0], bbox[1], midLat, midLon}, // SW
		{bbox[0], midLon, midLat, bbox[3]}, // SE
		{midLat, bbox[1], bbox[2], midLon}, // NW
		{midLat, midLon, bbox[2], bbox[3]}, // NE
	}
}

type overpassResponse struct {
	Elements []overpassElement `json:"elements"`
	// Remark carries server-side notices; a runtime timeout/OOM after execution
	// starts arrives here on an HTTP 200 with partial elements (see A2 fix).
	Remark string `json:"remark"`
}

type overpassGeometryPoint struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

type overpassRelationMember struct {
	Type     string                  `json:"type"`
	Ref      int64                   `json:"ref"`
	Role     string                  `json:"role"`
	Geometry []overpassGeometryPoint `json:"geometry,omitempty"`
}

type overpassElement struct {
	Type     string                   `json:"type"`
	ID       int64                    `json:"id"`
	Lat      float64                  `json:"lat,omitempty"`
	Lon      float64                  `json:"lon,omitempty"`
	Tags     map[string]string        `json:"tags,omitempty"`
	Nodes    []int64                  `json:"nodes,omitempty"`
	Geometry []overpassGeometryPoint  `json:"geometry,omitempty"`
	Members  []overpassRelationMember `json:"members,omitempty"`
}

func parseOverpassResponse(ctx context.Context, data []byte, resourceType resource.Type) ([]db.Feature, error) {
	var resp overpassResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse overpass json: %w", err)
	}
	if remarkIndicatesTruncation(resp.Remark) {
		return nil, fmt.Errorf("overpass remark %q: %w", strings.TrimSpace(resp.Remark), errOverpassTruncated)
	}

	// Build node index for resolving way geometries
	nodes := make(map[int64][2]float64)
	for _, e := range resp.Elements {
		if e.Type == "node" {
			nodes[e.ID] = [2]float64{e.Lon, e.Lat}
		}
	}

	var features []db.Feature
	for _, e := range resp.Elements {
		switch e.Type {
		case elementWay:
			if f, ok := buildFeatureFromWay(e, nodes, resourceType); ok {
				features = append(features, f)
			}
		case elementRelation:
			// Multipolygon relations (e.g. amenity=parking lots with
			// landscaping islands) carry the matching tag on the relation, not
			// the member ways, so the way clause never returns the members
			// separately — no double counting. Their geometry is inlined by
			// `out geom`, which relationToPolygons reads from e.Members.
			if f, ok := buildFeatureFromRelation(ctx, e, resourceType); ok {
				features = append(features, f)
			}
		}
	}

	return features, nil
}

func buildFeatureFromWay(e overpassElement, nodes map[int64][2]float64, resourceType resource.Type) (db.Feature, bool) {
	coords := resolveWayCoords(e, nodes)
	if len(coords) < 2 {
		return db.Feature{}, false
	}

	geojsonType := geomLineString
	if len(coords) >= 4 && coords[0] == coords[len(coords)-1] {
		if e.Tags["highway"] == "" || e.Tags["area"] == "yes" {
			geojsonType = geomPolygon
		}
	}

	var geojson string
	if geojsonType == geomPolygon {
		geojson = coordsToPolygonGeoJSON(coords)
	} else {
		geojson = coordsToLineStringGeoJSON(coords)
	}

	name := e.Tags["name"]
	if name == "" {
		name = e.Tags["highway"]
	}

	return db.Feature{
		ID:           fmt.Sprintf("osm:way:%d", e.ID),
		ResourceType: resourceType,
		Name:         name,
		Tags:         e.Tags,
		GeometryJSON: geojson,
		SourceAPI:    "overpass",
		FetchedAt:    time.Now(),
	}, true
}

// buildFeatureFromRelation converts a multipolygon relation into a single
// MultiPolygon feature, stitching its member ways (inlined by `out geom`) into
// outer rings with holes. Returns false when no closed ring can be formed.
// acceptOuter is nil: the relation already matched the resource's tag filter at
// the query level, so every closed outer ring belongs to the result.
func buildFeatureFromRelation(ctx context.Context, e overpassElement, resourceType resource.Type) (db.Feature, bool) {
	polys := relationToPolygons(ctx, e, stitchRings, nil)
	if len(polys) == 0 {
		return db.Feature{}, false
	}

	name := e.Tags["name"]
	if name == "" {
		name = e.Tags["amenity"]
	}

	return db.Feature{
		ID:           fmt.Sprintf("osm:relation:%d", e.ID),
		ResourceType: resourceType,
		Name:         name,
		Tags:         e.Tags,
		GeometryJSON: polysToMultiPolygonGeoJSON(polys),
		SourceAPI:    "overpass",
		FetchedAt:    time.Now(),
	}, true
}

func resolveWayCoords(e overpassElement, nodes map[int64][2]float64) [][2]float64 {
	// Try geometry field first (if out geom was used)
	if len(e.Geometry) > 0 {
		coords := make([][2]float64, len(e.Geometry))
		for i, g := range e.Geometry {
			coords[i] = [2]float64{g.Lon, g.Lat}
		}
		return coords
	}

	// Fall back to resolving node IDs
	coords := make([][2]float64, 0, len(e.Nodes))
	for _, nid := range e.Nodes {
		if c, ok := nodes[nid]; ok {
			coords = append(coords, c)
		}
	}
	return coords
}

func coordsToLineStringGeoJSON(coords [][2]float64) string {
	parts := make([]string, len(coords))
	for i, c := range coords {
		parts[i] = fmt.Sprintf("[%s,%s]", strconv.FormatFloat(c[0], 'f', 7, 64), strconv.FormatFloat(c[1], 'f', 7, 64))
	}
	return fmt.Sprintf(`{"type":"LineString","coordinates":[%s]}`, strings.Join(parts, ","))
}

func coordsToPolygonGeoJSON(coords [][2]float64) string {
	parts := make([]string, len(coords))
	for i, c := range coords {
		parts[i] = fmt.Sprintf("[%s,%s]", strconv.FormatFloat(c[0], 'f', 7, 64), strconv.FormatFloat(c[1], 'f', 7, 64))
	}
	return fmt.Sprintf(`{"type":"Polygon","coordinates":[[%s]]}`, strings.Join(parts, ","))
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
