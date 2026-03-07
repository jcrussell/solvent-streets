package ingest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"pvmt/internal/db"
	"pvmt/internal/resource"
)

const overpassAPI = "https://overpass-api.de/api/interpreter"

type OverpassSource struct {
	BBox [4]float64 // [south, west, north, east]
}

func (s *OverpassSource) Name() string { return "overpass" }

func (s *OverpassSource) Fetch(client *http.Client, rt resource.ResourceType) ([]db.Feature, error) {
	query := rt.OverpassQuery(s.BBox)

	resp, err := client.PostForm(overpassAPI, url.Values{"data": {query}})
	if err != nil {
		return nil, fmt.Errorf("overpass request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read overpass response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("overpass returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	return parseOverpassResponse(body, rt.Name())
}

type overpassResponse struct {
	Elements []overpassElement `json:"elements"`
}

type overpassElement struct {
	Type string            `json:"type"`
	ID   int64             `json:"id"`
	Lat  float64           `json:"lat,omitempty"`
	Lon  float64           `json:"lon,omitempty"`
	Tags map[string]string `json:"tags,omitempty"`
	Nodes []int64          `json:"nodes,omitempty"`
	Geometry []struct {
		Lat float64 `json:"lat"`
		Lon float64 `json:"lon"`
	} `json:"geometry,omitempty"`
}

func parseOverpassResponse(data []byte, resourceType string) ([]db.Feature, error) {
	var resp overpassResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse overpass json: %w", err)
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
		if e.Type != "way" {
			continue
		}

		coords := resolveWayCoords(e, nodes)
		if len(coords) < 2 {
			continue
		}

		geojsonType := "LineString"
		// If first and last nodes are the same, it's a polygon
		if len(coords) >= 4 && coords[0] == coords[len(coords)-1] {
			geojsonType = "Polygon"
		}

		var geojson string
		if geojsonType == "Polygon" {
			geojson = coordsToPolygonGeoJSON(coords)
		} else {
			geojson = coordsToLineStringGeoJSON(coords)
		}

		name := e.Tags["name"]
		if name == "" {
			name = e.Tags["highway"]
		}

		features = append(features, db.Feature{
			ID:           fmt.Sprintf("osm:way:%d", e.ID),
			ResourceType: resourceType,
			Name:         name,
			Tags:         e.Tags,
			GeometryJSON: geojson,
			SourceAPI:    "overpass",
			FetchedAt:    time.Now(),
		})
	}

	return features, nil
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
