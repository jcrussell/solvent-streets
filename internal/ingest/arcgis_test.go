package ingest

import (
	"testing"
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
	features, err := parseArcGISGeoJSON([]byte(data), "pavements")
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
	if f.ResourceType != "pavements" {
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
	features, err := parseArcGISGeoJSON([]byte(data), "pavements")
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
		features, err := parseArcGISGeoJSON([]byte(data), "pavements")
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
	features, err := parseArcGISGeoJSON([]byte(data), "pavements")
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 0 {
		t.Errorf("expected 0 features for missing geometry, got %d", len(features))
	}
}

func TestParseArcGISGeoJSON_EmptyFeatures(t *testing.T) {
	data := `{"features": []}`
	features, err := parseArcGISGeoJSON([]byte(data), "pavements")
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
	features, err := parseArcGISGeoJSON([]byte(data), "pavements")
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
