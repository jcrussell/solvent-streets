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

// FetchOSMWater queries Overpass for OSM `natural=water` polygons inside
// bbox and returns a single GeoJSON MultiPolygon string. Closed
// natural=water ways and natural=water multipolygon relations (whose
// outer/inner member ways are stitched into rings here) are both
// supported. Empty water responses return ("", nil) — callers must treat
// this as a benign no-op (no water in the bbox).
//
// Open `natural=coastline` linestrings that bound the open sea but only
// form polygons after closing along the bbox are not yet covered;
// that work lives in a follow-up bead.
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
		`[out:json][timeout:60];(way["natural"="water"](%f,%f,%f,%f);relation["natural"="water"](%f,%f,%f,%f););out geom;`,
		bbox[0], bbox[1], bbox[2], bbox[3],
		bbox[0], bbox[1], bbox[2], bbox[3],
	)
}

// waterPolygon is an outer ring with optional holes (lon/lat pairs).
type waterPolygon struct {
	outer [][2]float64
	holes [][][2]float64
}

func parseWaterResponse(data []byte) (string, error) {
	var resp overpassResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("parse overpass water json: %w", err)
	}

	var polys []waterPolygon
	for _, e := range resp.Elements {
		switch e.Type {
		case elementWay:
			coords := resolveWayCoords(e, nil)
			if !isClosedRing(coords) {
				continue
			}
			polys = append(polys, waterPolygon{outer: coords})
		case elementRelation:
			polys = append(polys, polygonsFromRelation(e)...)
		}
	}

	if len(polys) == 0 {
		return "", nil
	}
	return polysToMultiPolygonGeoJSON(polys), nil
}

func polygonsFromRelation(e overpassElement) []waterPolygon {
	var outerWays, innerWays [][][2]float64
	for _, m := range e.Members {
		if m.Type != elementWay || len(m.Geometry) < 2 {
			continue
		}
		coords := make([][2]float64, len(m.Geometry))
		for i, g := range m.Geometry {
			coords[i] = [2]float64{g.Lon, g.Lat}
		}
		switch m.Role {
		case "outer", "":
			// Per the OSM multipolygon spec the role should always be
			// "outer" or "inner", but very old relations sometimes have
			// blank roles — treat those as outer (the spec's default).
			outerWays = append(outerWays, coords)
		case "inner":
			innerWays = append(innerWays, coords)
		}
	}

	outerRings := stitchRings(outerWays)
	innerRings := stitchRings(innerWays)

	polys := make([]waterPolygon, 0, len(outerRings))
	for _, o := range outerRings {
		polys = append(polys, waterPolygon{outer: o})
	}
	for _, h := range innerRings {
		// Assign each inner ring to the first outer ring that contains
		// its first vertex. For our use (subtracting the union from a
		// city boundary), any containing outer gives the right union, so
		// the first-match is sufficient.
		for i, p := range polys {
			if pointInRing(h[0], p.outer) {
				polys[i].holes = append(polys[i].holes, h)
				break
			}
		}
	}
	return polys
}

// stitchRings chains open ways into closed rings by matching endpoints.
// Ways that cannot be closed are dropped. Each input way is consumed at
// most once. Time complexity is O(n²) in the number of ways, which is
// fine for the dozens-of-segments-per-relation scale of OSM water.
func stitchRings(ways [][][2]float64) [][][2]float64 {
	used := make([]bool, len(ways))
	var rings [][][2]float64

	for i := range ways {
		if used[i] || len(ways[i]) < 2 {
			continue
		}
		used[i] = true
		ring := append([][2]float64{}, ways[i]...)

		for ring[0] != ring[len(ring)-1] {
			extended, next := extendRing(ring, ways, used)
			if !extended {
				break
			}
			ring = next
		}

		if isClosedRing(ring) {
			rings = append(rings, ring)
		}
	}
	return rings
}

// extendRing finds an unused way whose endpoint matches ring's tail and
// appends it to ring (reversing the way if needed). The matched way is
// marked used. Returns (false, ring) when no way matches.
func extendRing(ring [][2]float64, ways [][][2]float64, used []bool) (bool, [][2]float64) {
	tail := ring[len(ring)-1]
	for j := range ways {
		if used[j] || len(ways[j]) < 2 {
			continue
		}
		w := ways[j]
		switch {
		case tail == w[0]:
			ring = append(ring, w[1:]...)
		case tail == w[len(w)-1]:
			for k := len(w) - 2; k >= 0; k-- {
				ring = append(ring, w[k])
			}
		default:
			continue
		}
		used[j] = true
		return true, ring
	}
	return false, ring
}

func isClosedRing(coords [][2]float64) bool {
	return len(coords) >= 4 && coords[0] == coords[len(coords)-1]
}

// pointInRing returns true if p is strictly inside ring using ray casting.
// The ring is in lon/lat order; that's fine because point-in-polygon is
// topological — it does not require an equal-area projection.
func pointInRing(p [2]float64, ring [][2]float64) bool {
	if len(ring) < 4 {
		return false
	}
	x, y := p[0], p[1]
	inside := false
	for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
		xi, yi := ring[i][0], ring[i][1]
		xj, yj := ring[j][0], ring[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			inside = !inside
		}
	}
	return inside
}

func polysToMultiPolygonGeoJSON(polys []waterPolygon) string {
	parts := make([]string, len(polys))
	for i, p := range polys {
		rings := make([]string, 0, 1+len(p.holes))
		rings = append(rings, coordRingJSON(p.outer))
		for _, h := range p.holes {
			rings = append(rings, coordRingJSON(h))
		}
		parts[i] = "[" + strings.Join(rings, ",") + "]"
	}
	return fmt.Sprintf(`{"type":"MultiPolygon","coordinates":[%s]}`, strings.Join(parts, ","))
}

func coordRingJSON(coords [][2]float64) string {
	parts := make([]string, len(coords))
	for i, c := range coords {
		parts[i] = fmt.Sprintf("[%g,%g]", c[0], c[1])
	}
	return "[" + strings.Join(parts, ",") + "]"
}
