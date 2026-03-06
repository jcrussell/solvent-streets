package ingest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"pvmt/internal/db"
	"pvmt/internal/resource"
)

// Alameda County ArcGIS feature service for city boundaries and road centerlines
const (
	arcgisBaseURL = "https://services5.arcgis.com/ROBnTHSNjoZ2Wm1P/arcgis/rest/services"
	arcgisCenterlines = arcgisBaseURL + "/Alameda_County_Street_Centerlines/FeatureServer/0/query"
)

type ArcGISSource struct{}

func (s *ArcGISSource) Name() string { return "arcgis" }

func (s *ArcGISSource) Fetch(client *http.Client, rt resource.ResourceType) ([]db.Feature, error) {
	// Only fetch centerlines for pavement type
	if rt.Name() != "pavements" {
		return nil, nil
	}

	bbox := LivermoreBBox
	envelope := fmt.Sprintf("%f,%f,%f,%f", bbox[1], bbox[0], bbox[3], bbox[2])

	params := url.Values{
		"where":          {"1=1"},
		"geometry":       {envelope},
		"geometryType":   {"esriGeometryEnvelope"},
		"inSR":           {"4326"},
		"outSR":          {"4326"},
		"outFields":      {"*"},
		"f":              {"geojson"},
		"resultRecordCount": {"5000"},
	}

	reqURL := arcgisCenterlines + "?" + params.Encode()
	resp, err := client.Get(reqURL)
	if err != nil {
		return nil, fmt.Errorf("arcgis request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read arcgis response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("arcgis returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	return parseArcGISGeoJSON(body, rt.Name())
}

type arcgisGeoJSON struct {
	Features []struct {
		Properties map[string]any `json:"properties"`
		Geometry   json.RawMessage `json:"geometry"`
	} `json:"features"`
}

func parseArcGISGeoJSON(data []byte, resourceType string) ([]db.Feature, error) {
	var resp arcgisGeoJSON
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse arcgis json: %w", err)
	}

	var features []db.Feature
	for i, f := range resp.Features {
		if f.Geometry == nil {
			continue
		}

		tags := make(map[string]string)
		var name string
		for k, v := range f.Properties {
			if v != nil {
				tags[k] = fmt.Sprintf("%v", v)
			}
			if k == "FULLNAME" || k == "FullName" || k == "fullname" {
				name = fmt.Sprintf("%v", v)
			}
		}

		id := fmt.Sprintf("arcgis:%d", i)
		if oid, ok := f.Properties["OBJECTID"]; ok {
			id = fmt.Sprintf("arcgis:%v", oid)
		}

		features = append(features, db.Feature{
			ID:           id,
			ResourceType: resourceType,
			Name:         name,
			Tags:         tags,
			GeometryJSON: string(f.Geometry),
			SourceAPI:    "arcgis",
			FetchedAt:    time.Now(),
		})
	}

	return features, nil
}
