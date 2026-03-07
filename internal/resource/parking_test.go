package resource

import (
	"strings"
	"testing"
)

func TestParking_Name(t *testing.T) {
	p := &Parking{}
	if p.Name() != "parking" {
		t.Errorf("expected parking, got %s", p.Name())
	}
}

func TestParking_OverpassQuery(t *testing.T) {
	p := &Parking{}
	q := p.OverpassQuery([4]float64{37.64, -121.84, 37.72, -121.68})
	if !strings.Contains(q, "amenity") || !strings.Contains(q, "parking") {
		t.Error("query should contain amenity=parking")
	}
}

func TestParking_ProcessFeatures_Polygon(t *testing.T) {
	features := []Feature{
		{
			ID:   "p1",
			Name: "Lot A",
			Tags: map[string]string{},
			GeometryJSON: `{"type":"Polygon","coordinates":[[[-121.7700,37.6800],[-121.7690,37.6800],[-121.7690,37.6810],[-121.7700,37.6810],[-121.7700,37.6800]]]}`,
		},
	}
	p := &Parking{}
	_, area, err := p.ProcessFeatures(features, testProj)
	if err != nil {
		t.Fatal(err)
	}
	if area <= 0 {
		t.Errorf("expected positive area, got %f", area)
	}
}

func TestParking_ProcessFeatures_LineStringSkipped(t *testing.T) {
	features := []Feature{
		{
			ID:           "ls1",
			Tags:         map[string]string{},
			GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6800],[-121.7690,37.6810]]}`,
		},
	}
	p := &Parking{}
	_, _, err := p.ProcessFeatures(features, testProj)
	if err == nil {
		t.Error("expected error when only LineString features (no polygons)")
	}
}

func TestParking_ProcessFeatures_Empty(t *testing.T) {
	p := &Parking{}
	_, _, err := p.ProcessFeatures(nil, testProj)
	if err == nil {
		t.Error("expected error for empty features")
	}
}
