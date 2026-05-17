package ingest

import (
	"encoding/json"
	"strings"
	"testing"
)

var testBBox = [4]float64{37.64, -121.84, 37.72, -121.68}

func TestAllSources(t *testing.T) {
	sources := AllSources(testBBox, "", Options{})
	if len(sources) != 1 {
		t.Fatalf("expected 1 source without arcgis URL, got %d", len(sources))
	}
	sources = AllSources(testBBox, "https://example.com/arcgis", Options{})
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources with arcgis URL, got %d", len(sources))
	}
}

func TestSourceByName_Overpass(t *testing.T) {
	src, err := SourceByName("overpass", testBBox, "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if src.Name() != "overpass" {
		t.Errorf("expected overpass, got %s", src.Name())
	}
}

func TestSourceByName_Unknown(t *testing.T) {
	_, err := SourceByName("bogus", testBBox, "", Options{})
	if err == nil {
		t.Error("expected error for unknown source")
	}
}

// closedWayJSON builds a minimal Overpass JSON response with one closed way.
func closedWayJSON(tags map[string]string) []byte {
	type geomPt struct {
		Lat float64 `json:"lat"`
		Lon float64 `json:"lon"`
	}
	ring := []geomPt{
		{37.7, -121.8}, {37.7, -121.7}, {37.6, -121.7}, {37.6, -121.8}, {37.7, -121.8},
	}
	type elem struct {
		Type     string            `json:"type"`
		ID       int64             `json:"id"`
		Tags     map[string]string `json:"tags"`
		Geometry []geomPt          `json:"geometry"`
	}
	resp := struct {
		Elements []elem `json:"elements"`
	}{
		Elements: []elem{{Type: "way", ID: 1, Tags: tags, Geometry: ring}},
	}
	b, _ := json.Marshal(resp)
	return b
}

func TestClosedHighwayWay_IsLineString(t *testing.T) {
	data := closedWayJSON(map[string]string{"highway": "residential"})
	features, err := parseOverpassResponse(data, testResourceRoads)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	if !strings.Contains(features[0].GeometryJSON, "LineString") {
		t.Errorf("closed highway way should be LineString, got: %s", features[0].GeometryJSON)
	}
}

func TestClosedHighwayAreaYes_IsPolygon(t *testing.T) {
	data := closedWayJSON(map[string]string{"highway": "pedestrian", "area": "yes"})
	features, err := parseOverpassResponse(data, testResourceRoads)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	if !strings.Contains(features[0].GeometryJSON, "Polygon") {
		t.Errorf("highway with area=yes should be Polygon, got: %s", features[0].GeometryJSON)
	}
}

func TestClosedNonHighwayWay_IsPolygon(t *testing.T) {
	data := closedWayJSON(map[string]string{"amenity": "parking"})
	features, err := parseOverpassResponse(data, testResourceParking)
	if err != nil {
		t.Fatal(err)
	}
	if len(features) != 1 {
		t.Fatalf("expected 1 feature, got %d", len(features))
	}
	if !strings.Contains(features[0].GeometryJSON, "Polygon") {
		t.Errorf("closed non-highway way should be Polygon, got: %s", features[0].GeometryJSON)
	}
}
