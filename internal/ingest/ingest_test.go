package ingest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

var testBBox = [4]float64{37.64, -121.84, 37.72, -121.68}

func sourceNames(sources []Source) []string {
	names := make([]string, len(sources))
	for i, s := range sources {
		names[i] = s.Name()
	}
	return names
}

func TestAllSources(t *testing.T) {
	cases := []struct {
		name      string
		overpass  bool
		arcgisURL string
		want      []string
	}{
		{"overpass only", true, "", []string{"overpass"}},
		{"overpass + arcgis", true, "https://example.com/arcgis", []string{"overpass", "arcgis"}},
		// overpass=false must OMIT OverpassSource — this is the whole point of
		// making the flag meaningful (config validation forbids the empty case).
		{"arcgis only, overpass off", false, "https://example.com/arcgis", []string{"arcgis"}},
		{"nothing configured", false, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sourceNames(AllSources(testBBox, tc.overpass, tc.arcgisURL, Options{}))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("AllSources names = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSourceByName_Overpass(t *testing.T) {
	src, err := SourceByName("overpass", testBBox, true, "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if src.Name() != "overpass" {
		t.Errorf("expected overpass, got %s", src.Name())
	}
}

// TestSourceByName_OverpassDisabled pins that requesting overpass explicitly on
// a city with overpass=false is rejected, since the source isn't assembled.
func TestSourceByName_OverpassDisabled(t *testing.T) {
	_, err := SourceByName("overpass", testBBox, false, "https://example.com/arcgis", Options{})
	if err == nil {
		t.Error("expected error requesting overpass when overpass=false")
	}
}

func TestSourceByName_Unknown(t *testing.T) {
	_, err := SourceByName("bogus", testBBox, true, "", Options{})
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
	features, err := parseOverpassResponse(context.Background(), data, testResourceRoads)
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
	features, err := parseOverpassResponse(context.Background(), data, testResourceRoads)
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
	features, err := parseOverpassResponse(context.Background(), data, testResourceParking)
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
