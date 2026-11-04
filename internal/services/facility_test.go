package services

import (
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/resource"
)

// TestFacilities: ingested Point rows come first with their own ids (any
// other geometry is skipped), then this service's configured entries under
// position-based ids, with a duplicate position kept once and other
// services' entries ignored.
func TestFacilities(t *testing.T) {
	rows := []resource.Feature{
		{ID: "osm:node:1", Name: "Station 1", GeometryJSON: `{"type":"Point","coordinates":[-121.7,37.6]}`},
		{ID: "osm:way:2", GeometryJSON: `{"type":"LineString","coordinates":[[0,0],[1,1]]}`},
	}
	configured := []config.FacilityConfig{
		{Service: "fire", Name: "Manual", Lon: -121.8, Lat: 37.7},
		{Service: "police", Name: "Other service", Lon: -121.9, Lat: 37.8},
		{Service: "fire", Name: "Same spot", Lon: -121.8, Lat: 37.7},
	}
	got := Facilities(rows, configured, "fire")
	if len(got) != 2 {
		t.Fatalf("facilities = %+v, want the OSM point and one config entry", got)
	}
	if got[0].ID != "osm:node:1" || got[0].Lon != -121.7 || got[0].Source != "osm" {
		t.Errorf("osm facility = %+v", got[0])
	}
	if want := ConfigFacilityID("fire", -121.8, 37.7); got[1].ID != want || got[1].Name != "Manual" || got[1].Source != "config" {
		t.Errorf("config facility = %+v, want id %s named Manual", got[1], want)
	}
}
