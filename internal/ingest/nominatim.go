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
	u := baseURL + "?" + url.Values{
		"q":               {cityName},
		"format":          {"json"},
		"limit":           {"5"},
		"polygon_geojson": {"1"},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", fmt.Errorf("create nominatim request: %w", err)
	}
	req.Header.Set("User-Agent", "pvmt/1.0 (https://github.com/jcrussell/solvent-streets)")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("nominatim request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("nominatim returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
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

	// Pick the first result that is a city/town, falling back to the first result
	best := 0
	for i, r := range results {
		if cityAddressTypes[r.AddressType] {
			best = i
			break
		}
	}

	// Validate geometry type
	var geomType struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(results[best].GeoJSON, &geomType); err != nil {
		return "", fmt.Errorf("parse geometry type: %w", err)
	}
	if geomType.Type != geomPolygon && geomType.Type != geomMultiPolygon {
		return "", fmt.Errorf("expected Polygon or MultiPolygon, got %q", geomType.Type)
	}

	return string(results[best].GeoJSON), nil
}
