package ingest

import (
	"encoding/json"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/resource"
)

const (
	testGeomLineString = "LineString"
	testGeomPolygon    = "Polygon"
)

var (
	testResourceRoads   = resource.KindRoads.WithScope(resource.ScopeAll)
	testResourceParking = resource.KindParking.WithScope(resource.ScopeAll)
)

func TestParseOverpassResponse_BasicWayWithGeometry(t *testing.T) {
	data := `{
		"elements": [
			{
				"type": "way",
				"id": 12345,
				"tags": {"name": "Main St", "highway": "residential"},
				"geometry": [
					{"lat": 37.68, "lon": -121.77},
					{"lat": 37.69, "lon": -121.76}
				]
			}
		]
	}`
	features, err := parseOverpassResponse([]byte(data), testResourceRoads)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	f := features[0]
	if f.ID != "osm:way:12345" {
		t.Errorf("expected id osm:way:12345, got %s", f.ID)
	}
	if f.Name != "Main St" {
		t.Errorf("expected name Main St, got %s", f.Name)
	}
	if f.ResourceType != testResourceRoads {
		t.Errorf("expected resource type pavements, got %s", f.ResourceType)
	}
	// Should be LineString since first != last
	var geojson struct{ Type string }
	if err := json.Unmarshal([]byte(f.GeometryJSON), &geojson); err != nil {
		t.Fatal(err)
	}
	if geojson.Type != testGeomLineString {
		t.Errorf("expected LineString, got %s", geojson.Type)
	}
}

func TestParseOverpassResponse_WayResolvedViaNodeIndex(t *testing.T) {
	data := `{
		"elements": [
			{"type": "node", "id": 1, "lat": 37.68, "lon": -121.77},
			{"type": "node", "id": 2, "lat": 37.69, "lon": -121.76},
			{
				"type": "way",
				"id": 100,
				"tags": {"name": "Oak Ave"},
				"nodes": [1, 2]
			}
		]
	}`
	features, err := parseOverpassResponse([]byte(data), testResourceRoads)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	if features[0].ID != "osm:way:100" {
		t.Errorf("expected osm:way:100, got %s", features[0].ID)
	}
}

func TestParseOverpassResponse_ClosedPolygon(t *testing.T) {
	data := `{
		"elements": [
			{
				"type": "way",
				"id": 200,
				"tags": {"name": "Parking Lot"},
				"geometry": [
					{"lat": 37.68, "lon": -121.77},
					{"lat": 37.68, "lon": -121.76},
					{"lat": 37.69, "lon": -121.76},
					{"lat": 37.69, "lon": -121.77},
					{"lat": 37.68, "lon": -121.77}
				]
			}
		]
	}`
	features, err := parseOverpassResponse([]byte(data), testResourceParking)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	var geojson struct{ Type string }
	if err := json.Unmarshal([]byte(features[0].GeometryJSON), &geojson); err != nil {
		t.Fatal(err)
	}
	if geojson.Type != testGeomPolygon {
		t.Errorf("expected Polygon, got %s", geojson.Type)
	}
}

func TestParseOverpassResponse_WayLessThan2Coords(t *testing.T) {
	data := `{
		"elements": [
			{
				"type": "way",
				"id": 300,
				"tags": {"name": "Short"},
				"geometry": [{"lat": 37.68, "lon": -121.77}]
			}
		]
	}`
	features, err := parseOverpassResponse([]byte(data), testResourceRoads)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 0 {
		t.Errorf("expected 0 features for <2 coords, got %d", len(features))
	}
}

func TestParseOverpassResponse_EmptyResponse(t *testing.T) {
	data := `{"elements": []}`
	features, err := parseOverpassResponse([]byte(data), testResourceRoads)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 0 {
		t.Errorf("expected 0 features, got %d", len(features))
	}
}

func TestParseOverpassResponse_NameFallbackToHighway(t *testing.T) {
	data := `{
		"elements": [
			{
				"type": "way",
				"id": 400,
				"tags": {"highway": "residential"},
				"geometry": [
					{"lat": 37.68, "lon": -121.77},
					{"lat": 37.69, "lon": -121.76}
				]
			}
		]
	}`
	features, err := parseOverpassResponse([]byte(data), testResourceRoads)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	if features[0].Name != "residential" {
		t.Errorf("expected name fallback to 'residential', got %s", features[0].Name)
	}
}

func TestParseOverpassResponse_GeometryPriorityOverNodes(t *testing.T) {
	data := `{
		"elements": [
			{"type": "node", "id": 1, "lat": 0.0, "lon": 0.0},
			{"type": "node", "id": 2, "lat": 0.0, "lon": 0.0},
			{
				"type": "way",
				"id": 500,
				"tags": {"name": "Test"},
				"nodes": [1, 2],
				"geometry": [
					{"lat": 37.68, "lon": -121.77},
					{"lat": 37.69, "lon": -121.76}
				]
			}
		]
	}`
	features, err := parseOverpassResponse([]byte(data), testResourceRoads)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	// Geometry field should take priority; coords should be from geometry, not nodes
	var geojson struct {
		Coordinates [][2]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(features[0].GeometryJSON), &geojson); err != nil {
		t.Fatal(err)
	}
	if len(geojson.Coordinates) != 2 {
		t.Fatalf("expected 2 coords, got %d", len(geojson.Coordinates))
	}
	// First coord lon should be -121.77 (from geometry), not 0.0 (from nodes)
	if geojson.Coordinates[0][0] != -121.77 {
		t.Errorf("expected lon -121.77, got %f", geojson.Coordinates[0][0])
	}
}

func TestCoordsToLineStringGeoJSON(t *testing.T) {
	coords := [][2]float64{{-121.77, 37.68}, {-121.76, 37.69}}
	result := coordsToLineStringGeoJSON(coords)
	var obj struct {
		Type        string       `json:"type"`
		Coordinates [][2]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(result), &obj); err != nil {
		t.Fatal(err)
	}
	if obj.Type != testGeomLineString {
		t.Errorf("expected type LineString, got %s", obj.Type)
	}
	if len(obj.Coordinates) != 2 {
		t.Errorf("expected 2 coordinates, got %d", len(obj.Coordinates))
	}
}

func TestCoordsToPolygonGeoJSON(t *testing.T) {
	coords := [][2]float64{
		{-121.77, 37.68}, {-121.76, 37.68},
		{-121.76, 37.69}, {-121.77, 37.69},
		{-121.77, 37.68},
	}
	result := coordsToPolygonGeoJSON(coords)
	var obj struct {
		Type        string         `json:"type"`
		Coordinates [][][2]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal([]byte(result), &obj); err != nil {
		t.Fatal(err)
	}
	if obj.Type != testGeomPolygon {
		t.Errorf("expected type Polygon, got %s", obj.Type)
	}
	if len(obj.Coordinates) != 1 {
		t.Errorf("expected 1 ring, got %d", len(obj.Coordinates))
	}
	if len(obj.Coordinates[0]) != 5 {
		t.Errorf("expected 5 coords in ring, got %d", len(obj.Coordinates[0]))
	}
}
