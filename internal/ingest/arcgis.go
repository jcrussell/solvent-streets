package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

const arcgisMaxRecords = 5000
const arcgisMaxPages = 200 // safety limit: 200 pages × 5000 = 1M features max

// Default Alameda County ArcGIS feature service URL
const defaultArcGISCenterlines = "https://services5.arcgis.com/ROBnTHSNjoZ2Wm1P/arcgis/rest/services/Street_Centerlines/FeatureServer/0/query"

type ArcGISSource struct {
	BBox     [4]float64 // [south, west, north, east]
	URL      string     // custom ArcGIS endpoint; empty uses default
	Progress io.Writer  // pagination progress sink; nil discards
}

var _ Source = (*ArcGISSource)(nil)

func (s *ArcGISSource) progress() io.Writer {
	if s.Progress == nil {
		return io.Discard
	}
	return s.Progress
}

func (s *ArcGISSource) Name() string { return "arcgis" }

func (s *ArcGISSource) Fetch(ctx context.Context, client *http.Client, rt resource.Source) ([]db.Feature, error) {
	// Only fetch centerlines for road type
	if rt.Type() != resource.TypeRoads {
		return []db.Feature{}, nil
	}

	endpoint := s.URL
	if endpoint == "" {
		endpoint = defaultArcGISCenterlines
	}

	bbox := s.BBox
	envelope := fmt.Sprintf("%f,%f,%f,%f", bbox[1], bbox[0], bbox[3], bbox[2])

	var allFeatures []db.Feature
	offset := 0

	rtVal := rt.Type()
	for page := 0; ; page++ {
		if page >= arcgisMaxPages {
			return nil, fmt.Errorf("arcgis: exceeded %d pages (%d features), aborting", arcgisMaxPages, len(allFeatures))
		}
		features, err := fetchArcGISPage(ctx, client, endpoint, envelope, rtVal, offset)
		if err != nil {
			return nil, err
		}
		allFeatures = append(allFeatures, features...)

		if len(features) < arcgisMaxRecords {
			break // last page
		}
		offset += len(features)
		fmt.Fprintf(s.progress(), "ArcGIS: fetched %d features so far, requesting next page at offset %d...\n", len(allFeatures), offset)
	}

	return allFeatures, nil
}

func fetchArcGISPage(ctx context.Context, client *http.Client, endpoint, envelope string, resourceType resource.Type, offset int) ([]db.Feature, error) {
	params := url.Values{
		"where":             {"1=1"},
		"geometry":          {envelope},
		"geometryType":      {"esriGeometryEnvelope"},
		"inSR":              {"4326"},
		"outSR":             {"4326"},
		"outFields":         {"*"},
		"f":                 {"geojson"},
		"resultRecordCount": {strconv.Itoa(arcgisMaxRecords)},
		"resultOffset":      {strconv.Itoa(offset)},
	}

	reqURL := endpoint + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create arcgis request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("arcgis request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read arcgis response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("arcgis %s returned %d: %s", endpoint, resp.StatusCode, truncate(string(body), 200))
	}

	// ArcGIS sometimes returns service-level errors as HTTP 200 with a JSON
	// error envelope (e.g. stale layer path, retired service). Detect those
	// up front so the caller sees the underlying message + endpoint instead
	// of an empty feature list.
	if msg, ok := arcgisErrorMessage(body); ok {
		return nil, fmt.Errorf("arcgis %s: %s", endpoint, msg)
	}

	return parseArcGISGeoJSON(body, resourceType, offset)
}

// arcgisErrorMessage reports whether body is an ArcGIS error envelope of the
// form {"error":{"code":N,"message":"..."}} and returns a human-readable
// summary. Returns ok=false for any non-error response (including valid
// GeoJSON FeatureCollections, which have no "error" key).
func arcgisErrorMessage(body []byte) (string, bool) {
	var env struct {
		Error *struct {
			Code    int      `json:"code"`
			Message string   `json:"message"`
			Details []string `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.Error == nil {
		return "", false
	}
	msg := env.Error.Message
	if msg == "" {
		msg = "unknown error"
	}
	if len(env.Error.Details) > 0 && env.Error.Details[0] != msg {
		msg = fmt.Sprintf("%s (%s)", msg, env.Error.Details[0])
	}
	return fmt.Sprintf("code %d: %s", env.Error.Code, msg), true
}

type arcgisGeoJSON struct {
	Features []struct {
		Properties map[string]any  `json:"properties"`
		Geometry   json.RawMessage `json:"geometry"`
	} `json:"features"`
}

func parseArcGISGeoJSON(data []byte, resourceType resource.Type, baseIndex int) ([]db.Feature, error) {
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

		id := fmt.Sprintf("arcgis:%d", baseIndex+i)
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
