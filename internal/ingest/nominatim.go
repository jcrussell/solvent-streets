package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const nominatimBaseURL = "https://nominatim.openstreetmap.org/search"

// cityAddressTypes are Nominatim addresstype values that represent cities/towns,
// ordered by preference. Used to filter out county/state results.
var cityAddressTypes = map[string]bool{
	"city":         true,
	"town":         true,
	"village":      true,
	"municipality": true,
}

// FetchCityBoundary fetches a city boundary polygon from OSM Nominatim.
// Returns the GeoJSON geometry string (Polygon or MultiPolygon).
// Fetches multiple results and picks the first city/town match to avoid
// returning county or state boundaries (e.g., "Alameda, CA" → City of Alameda, not Alameda County).
func FetchCityBoundary(ctx context.Context, client *http.Client, cityName string) (string, error) {
	return fetchCityBoundary(ctx, client, nominatimBaseURL, cityName)
}

func fetchCityBoundary(ctx context.Context, client *http.Client, baseURL string, cityName string) (string, error) {
	// countrycodes=us constrains results to the United States. Without it,
	// Nominatim parses the state suffix in names like "Windsor, CA" or
	// "Richmond, CA" as the ISO country code CA (Canada) and returns
	// Windsor, Ontario / Richmond, British Columbia instead of the
	// California cities. All shipped configs are US cities; for a future
	// non-US city, pin the boundary via [[cities]].boundary_relation_id.
	u := baseURL + "?" + url.Values{
		"q":               {cityName},
		"format":          {"json"},
		"limit":           {"5"},
		"polygon_geojson": {"1"},
		"countrycodes":    {"us"},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", fmt.Errorf("create nominatim request: %w", err)
	}
	req.Header.Set("User-Agent", UserAgent())

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("nominatim request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("nominatim returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes))
	if err != nil {
		return "", fmt.Errorf("read nominatim response: %w", err)
	}

	var results []struct {
		AddressType string          `json:"addresstype"`
		GeoJSON     json.RawMessage `json:"geojson"`
	}
	if err := json.Unmarshal(body, &results); err != nil {
		return "", fmt.Errorf("parse nominatim response: %w", err)
	}

	if len(results) == 0 {
		return "", fmt.Errorf("nominatim returned no results for %q", cityName)
	}

	// Pick the first result that is BOTH a city/town addresstype AND has
	// Polygon/MultiPolygon geometry — Nominatim sometimes returns a
	// place=city node (Point geometry) tagged with addresstype=city for
	// cities like Albuquerque, NM, where the admin boundary lives only
	// in OSM as a relation. Fall back to "first polygon of any
	// addresstype" if no city-typed polygon exists, else error cleanly.
	for _, r := range results {
		if !cityAddressTypes[r.AddressType] {
			continue
		}
		if isPolygonGeometry(r.GeoJSON) {
			return string(r.GeoJSON), nil
		}
	}
	for _, r := range results {
		if isPolygonGeometry(r.GeoJSON) {
			return string(r.GeoJSON), nil
		}
	}
	return "", fmt.Errorf("nominatim returned no Polygon/MultiPolygon result for %q "+
		"(set [[cities]].boundary_relation_id to fetch the admin boundary from Overpass)", cityName)
}

// isPolygonGeometry reports whether the raw GeoJSON geometry is a Polygon
// or MultiPolygon. Returns false on parse error so callers can skip
// malformed results without failing the whole fetch.
func isPolygonGeometry(raw json.RawMessage) bool {
	var t struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return false
	}
	return t.Type == geomPolygon || t.Type == geomMultiPolygon
}
