package ingest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const nominatimBaseURL = "https://nominatim.openstreetmap.org/search"

// FetchCityBoundary fetches a city boundary polygon from OSM Nominatim.
// Returns the GeoJSON geometry string (Polygon or MultiPolygon).
func FetchCityBoundary(client *http.Client, cityName string) (string, error) {
	u := nominatimBaseURL + "?" + url.Values{
		"q":               {cityName},
		"format":          {"json"},
		"limit":           {"1"},
		"polygon_geojson": {"1"},
	}.Encode()

	return fetchFromURL(client, u, cityName)
}

// fetchFromURL performs the HTTP request and parses the Nominatim response.
func fetchFromURL(client *http.Client, u string, cityName string) (string, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return "", fmt.Errorf("create nominatim request: %w", err)
	}
	req.Header.Set("User-Agent", "pvmt/1.0 (https://github.com/solvent-streets/pvmt)")

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
		GeoJSON json.RawMessage `json:"geojson"`
	}
	if err := json.Unmarshal(body, &results); err != nil {
		return "", fmt.Errorf("parse nominatim response: %w", err)
	}

	if len(results) == 0 {
		return "", fmt.Errorf("nominatim returned no results for %q", cityName)
	}

	// Validate geometry type
	var geomType struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(results[0].GeoJSON, &geomType); err != nil {
		return "", fmt.Errorf("parse geometry type: %w", err)
	}
	if geomType.Type != "Polygon" && geomType.Type != "MultiPolygon" {
		return "", fmt.Errorf("expected Polygon or MultiPolygon, got %q", geomType.Type)
	}

	return string(results[0].GeoJSON), nil
}
