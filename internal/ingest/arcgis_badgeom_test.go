package ingest

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/logs"
)

// TestRowsFromArcGISPage_DropsGeometryTheStoreRefuses: a row whose GeoJSON
// the store would refuse is dropped with a warning naming the count and an
// example id, keeps its raw id for pagination, and the rest of the layer
// saves.
func TestRowsFromArcGISPage_DropsGeometryTheStoreRefuses(t *testing.T) {
	body := `{"features":[
		{"properties":{"OBJECTID":1,"FULLNAME":"Main St"},"geometry":{"type":"LineString","coordinates":[[-121.8,37.6],[-121.7,37.7]]}},
		{"properties":{"OBJECTID":2},"geometry":{"type":"Circle","coordinates":[0,0]}},
		{"properties":{"OBJECTID":3},"geometry":{"type":"LineString","coordinates":"nope"}},
		{"properties":{"OBJECTID":4,"FULLNAME":"Oak St"},"geometry":{"type":"Point","coordinates":[-121.75,37.65]}}
	]}`
	page, err := parseArcGISPage([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	ctx := logs.WithLogger(context.Background(), slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	rows, rawIDs := rowsFromArcGISPage(ctx, page, "OBJECTID", "arcgis", 0)
	if len(rawIDs) != 4 {
		t.Errorf("raw ids = %v, want all 4 (pagination counts the raw page)", rawIDs)
	}
	if len(rows) != 2 || rows[0].ID != "arcgis:1" || rows[1].ID != "arcgis:4" {
		t.Fatalf("rows = %+v, want arcgis:1 and arcgis:4", rows)
	}
	log := buf.String()
	if !strings.Contains(log, "unusable geometry") || !strings.Contains(log, "count=2") || !strings.Contains(log, "example_id=arcgis:2") {
		t.Errorf("warning = %q, want the dropped count and an example id", log)
	}

	root, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	id, err := root.EnsureCity(ctx, "c", "C", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := root.ForCity(id).UpsertFeatures(ctx, rtRoads, roadFeaturesFromRows(rows, rtRoads), nil); err != nil {
		t.Errorf("saving the kept rows: %v", err)
	}
}

// TestOverpassGeometryIsStorable: Overpass features are built here, not taken
// from upstream GeoJSON, so every shape the builders emit (line, closed-way
// polygon, relation multipolygon with a hole, facility point) must be one the
// store accepts; they are not filtered like ArcGIS rows.
func TestOverpassGeometryIsStorable(t *testing.T) {
	ctx := context.Background()
	data := `{"elements":[
		{"type":"way","id":1,"tags":{"highway":"residential"},"geometry":[{"lat":37.68,"lon":-121.77},{"lat":37.69,"lon":-121.76}]},
		{"type":"way","id":2,"tags":{"amenity":"parking"},"geometry":[{"lat":37.68,"lon":-121.77},{"lat":37.68,"lon":-121.76},{"lat":37.69,"lon":-121.76},{"lat":37.68,"lon":-121.77}]},
		{"type":"relation","id":3,"tags":{"amenity":"parking"},"members":[
			{"type":"way","ref":1,"role":"outer","geometry":[{"lat":37.680,"lon":-121.770},{"lat":37.680,"lon":-121.760},{"lat":37.690,"lon":-121.760},{"lat":37.690,"lon":-121.770},{"lat":37.680,"lon":-121.770}]},
			{"type":"way","ref":2,"role":"inner","geometry":[{"lat":37.683,"lon":-121.767},{"lat":37.683,"lon":-121.763},{"lat":37.687,"lon":-121.763},{"lat":37.683,"lon":-121.767}]}]}
	]}`
	features, err := parseOverpassResponse(ctx, []byte(data), testResourceParking)
	if err != nil {
		t.Fatal(err)
	}
	features = append(features, db.Feature{ID: "pt", GeometryJSON: mustGeoJSON(t)(coordToPointGeoJSON([2]float64{-121.75, 37.65}))})
	if len(features) != 4 {
		t.Fatalf("built %d features, want 4", len(features))
	}
	for _, f := range features {
		if err := db.CheckGeometry(f.GeometryJSON); err != nil {
			t.Errorf("%s: %s is refused by the store: %v", f.ID, f.GeometryJSON, err)
		}
	}
}
