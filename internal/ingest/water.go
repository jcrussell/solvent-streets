package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// FetchOSMWater queries Overpass for OSM `natural=water` ways inside bbox
// and returns a single GeoJSON MultiPolygon string assembled from each
// closed way. Empty water responses return ("", nil) — callers must
// treat this as a benign no-op (no water in the bbox).
//
// This function is intentionally limited to closed ways: relations
// (large multipolygon water bodies) and coastlines (open linestrings
// that bound the sea but do not form polygons on their own) are not
// yet supported. The follow-up bead expands coverage to those.
func FetchOSMWater(ctx context.Context, client *http.Client, bbox [4]float64) (string, error) {
	return fetchOSMWater(ctx, client, overpassAPI, bbox)
}

func fetchOSMWater(ctx context.Context, client *http.Client, baseURL string, bbox [4]float64) (string, error) {
	query := buildWaterQuery(bbox)

	req, err := http.NewRequestWithContext(AllowRetry(ctx), http.MethodPost, baseURL, strings.NewReader(url.Values{"data": {query}}.Encode()))
	if err != nil {
		return "", fmt.Errorf("create overpass water request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", UserAgent())

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("overpass water request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024*1024))
	if err != nil {
		return "", fmt.Errorf("read overpass water response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("overpass water returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	return parseWaterResponse(body)
}

func buildWaterQuery(bbox [4]float64) string {
	// bbox is [south, west, north, east] — Overpass expects the same order.
	return fmt.Sprintf(
		`[out:json][timeout:60];(way["natural"="water"](%f,%f,%f,%f);way["natural"="coastline"](%f,%f,%f,%f););out geom;`,
		bbox[0], bbox[1], bbox[2], bbox[3],
		bbox[0], bbox[1], bbox[2], bbox[3],
	)
}

func parseWaterResponse(data []byte) (string, error) {
	var resp overpassResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("parse overpass water json: %w", err)
	}

	var polygons [][][2]float64
	for _, e := range resp.Elements {
		if e.Type != "way" {
			continue
		}
		coords := resolveWayCoords(e, nil)
		if len(coords) < 4 {
			continue
		}
		// Only accept closed rings — open coastlines need stitching that
		// this V1 deliberately omits.
		if coords[0] != coords[len(coords)-1] {
			continue
		}
		polygons = append(polygons, coords)
	}

	if len(polygons) == 0 {
		return "", nil
	}
	return coordsToMultiPolygonGeoJSON(polygons), nil
}

func coordsToMultiPolygonGeoJSON(polys [][][2]float64) string {
	rings := make([]string, len(polys))
	for i, coords := range polys {
		rings[i] = "[" + coordRingJSON(coords) + "]"
	}
	return fmt.Sprintf(`{"type":"MultiPolygon","coordinates":[%s]}`, strings.Join(rings, ","))
}

func coordRingJSON(coords [][2]float64) string {
	parts := make([]string, len(coords))
	for i, c := range coords {
		parts[i] = fmt.Sprintf("[%g,%g]", c[0], c[1])
	}
	return "[" + strings.Join(parts, ",") + "]"
}
