package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"pvmt/internal/resource"
)

func TestParseArcGISGeoJSON_BasicFeature(t *testing.T) {
	data := `{
		"features": [
			{
				"properties": {"OBJECTID": 42, "FULLNAME": "First St"},
				"geometry": {"type": "LineString", "coordinates": [[-121.77, 37.68], [-121.76, 37.69]]}
			}
		]
	}`
	features, err := parseArcGISGeoJSON([]byte(data), "roads", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	f := features[0]
	if f.ID != "arcgis:42" {
		t.Errorf("expected id arcgis:42, got %s", f.ID)
	}
	if f.Name != "First St" {
		t.Errorf("expected name First St, got %s", f.Name)
	}
	if f.ResourceType != "roads" {
		t.Errorf("expected resource type pavements, got %s", f.ResourceType)
	}
}

func TestParseArcGISGeoJSON_NoOBJECTID(t *testing.T) {
	data := `{
		"features": [
			{
				"properties": {"FULLNAME": "Second St"},
				"geometry": {"type": "LineString", "coordinates": [[-121.77, 37.68]]}
			}
		]
	}`
	features, err := parseArcGISGeoJSON([]byte(data), "roads", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	if features[0].ID != "arcgis:0" {
		t.Errorf("expected fallback id arcgis:0, got %s", features[0].ID)
	}
}

func TestParseArcGISGeoJSON_FULLNAMEExtraction(t *testing.T) {
	for _, key := range []string{"FULLNAME", "FullName", "fullname"} {
		data := `{"features": [{"properties": {"` + key + `": "Third Ave"}, "geometry": {"type":"Point","coordinates":[-121.77,37.68]}}]}`
		features, err := parseArcGISGeoJSON([]byte(data), "roads", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(features) != 1 || features[0].Name != "Third Ave" {
			t.Errorf("key %s: expected name Third Ave, got %s", key, features[0].Name)
		}
	}
}

func TestParseArcGISGeoJSON_NullGeometry(t *testing.T) {
	// JSON null results in json.RawMessage("null") which is non-nil,
	// so the feature is included. Missing geometry field results in nil.
	data := `{
		"features": [
			{
				"properties": {"OBJECTID": 1}
			}
		]
	}`
	features, err := parseArcGISGeoJSON([]byte(data), "roads", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 0 {
		t.Errorf("expected 0 features for missing geometry, got %d", len(features))
	}
}

func TestParseArcGISGeoJSON_EmptyFeatures(t *testing.T) {
	data := `{"features": []}`
	features, err := parseArcGISGeoJSON([]byte(data), "roads", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 0 {
		t.Errorf("expected 0 features, got %d", len(features))
	}
}

func TestParseArcGISGeoJSON_NumericPropertyValues(t *testing.T) {
	data := `{
		"features": [
			{
				"properties": {"OBJECTID": 1, "SPEED_LIMIT": 35},
				"geometry": {"type": "Point", "coordinates": [-121.77, 37.68]}
			}
		]
	}`
	features, err := parseArcGISGeoJSON([]byte(data), "roads", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	if features[0].Tags["SPEED_LIMIT"] != "35" {
		t.Errorf("expected SPEED_LIMIT tag '35', got %q", features[0].Tags["SPEED_LIMIT"])
	}
}

func TestParseArcGISGeoJSON_BaseIndexOffset(t *testing.T) {
	// Features without OBJECTID should use baseIndex+i as the fallback ID.
	data := `{"features": [
		{"properties": {"FULLNAME": "A St"}, "geometry": {"type":"Point","coordinates":[-121.77,37.68]}},
		{"properties": {"FULLNAME": "B St"}, "geometry": {"type":"Point","coordinates":[-121.76,37.69]}}
	]}`
	features, err := parseArcGISGeoJSON([]byte(data), "roads", 5000)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 2 {
		t.Fatalf("expected 2 features, got %d", len(features))
	}
	if features[0].ID != "arcgis:5000" {
		t.Errorf("expected id arcgis:5000, got %s", features[0].ID)
	}
	if features[1].ID != "arcgis:5001" {
		t.Errorf("expected id arcgis:5001, got %s", features[1].ID)
	}
}

// makeArcGISFeatures builds a GeoJSON response with n features, each having
// an OBJECTID starting at startOID.
func makeArcGISFeatures(n, startOID int) []byte {
	type feat struct {
		Properties map[string]any  `json:"properties"`
		Geometry   json.RawMessage `json:"geometry"`
	}
	feats := make([]feat, n)
	for i := range feats {
		feats[i] = feat{
			Properties: map[string]any{"OBJECTID": startOID + i},
			Geometry:   json.RawMessage(`{"type":"Point","coordinates":[-121.77,37.68]}`),
		}
	}
	data, _ := json.Marshal(map[string]any{"features": feats})
	return data
}

func TestFetch_Pagination(t *testing.T) {
	// Simulate a server that returns arcgisMaxRecords on the first page
	// and a smaller set on the second page.
	const pageSize = arcgisMaxRecords
	const page2Size = 42

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		offset, _ := strconv.Atoi(r.URL.Query().Get("resultOffset"))
		var body []byte
		switch offset {
		case 0:
			body = makeArcGISFeatures(pageSize, 1)
		case pageSize:
			body = makeArcGISFeatures(page2Size, pageSize+1)
		default:
			t.Errorf("unexpected offset %d", offset)
			http.Error(w, "bad offset", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	src := &ArcGISSource{
		BBox: [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:  srv.URL,
	}
	rt := resource.ByName("roads")
	features, err := src.Fetch(context.Background(), srv.Client(), rt)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != pageSize+page2Size {
		t.Fatalf("expected %d features, got %d", pageSize+page2Size, len(features))
	}
	if calls != 2 {
		t.Errorf("expected 2 server calls, got %d", calls)
	}
	// Verify IDs from both pages are present.
	if features[0].ID != "arcgis:1" {
		t.Errorf("first feature id: want arcgis:1, got %s", features[0].ID)
	}
	if features[pageSize].ID != fmt.Sprintf("arcgis:%d", pageSize+1) {
		t.Errorf("first page-2 feature id: want arcgis:%d, got %s", pageSize+1, features[pageSize].ID)
	}
}

func TestFetch_SinglePage(t *testing.T) {
	// When the server returns fewer than arcgisMaxRecords, no second request.
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.Write(makeArcGISFeatures(10, 1))
	}))
	defer srv.Close()

	src := &ArcGISSource{
		BBox: [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:  srv.URL,
	}
	features, err := src.Fetch(context.Background(), srv.Client(), resource.ByName("roads"))
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 10 {
		t.Fatalf("expected 10 features, got %d", len(features))
	}
	if calls != 1 {
		t.Errorf("expected 1 server call, got %d", calls)
	}
}

func TestFetch_EmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"features": []}`))
	}))
	defer srv.Close()

	src := &ArcGISSource{
		BBox: [4]float64{37.0, -122.0, 38.0, -121.0},
		URL:  srv.URL,
	}
	features, err := src.Fetch(context.Background(), srv.Client(), resource.ByName("roads"))
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 0 {
		t.Errorf("expected 0 features, got %d", len(features))
	}
}
