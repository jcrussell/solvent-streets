package resource

import (
	"context"
	"strings"
	"testing"
)

func TestParking_Type(t *testing.T) {
	p := &Parking{}
	if p.Type() != TypeParking {
		t.Errorf("expected TypeParking, got %v", p.Type())
	}
}

func TestParking_OverpassQuery(t *testing.T) {
	p := &Parking{}
	q := p.OverpassQuery([4]float64{37.64, -121.84, 37.72, -121.68})
	if !strings.Contains(q, "amenity") || !strings.Contains(q, "parking") {
		t.Error("query should contain amenity=parking")
	}
}

func TestParking_BufferFeatures_Polygon(t *testing.T) {
	features := []Feature{
		{
			ID:           "p1",
			Name:         "Lot A",
			Tags:         map[string]string{},
			GeometryJSON: `{"type":"Polygon","coordinates":[[[-121.7700,37.6800],[-121.7690,37.6800],[-121.7690,37.6810],[-121.7700,37.6810],[-121.7700,37.6800]]]}`,
		},
	}
	p := &Parking{}
	geoms := Geoms(p.BufferFeaturesPaired(context.Background(), features, testProj))
	if len(geoms) != 1 {
		t.Errorf("expected 1 buffered geometry, got %d", len(geoms))
	}
	if geoms[0].Area() <= 0 {
		t.Errorf("expected positive area, got %f", geoms[0].Area())
	}
}

func TestParking_BufferFeatures_LineStringSkipped(t *testing.T) {
	features := []Feature{
		{
			ID:           "ls1",
			Tags:         map[string]string{},
			GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6800],[-121.7690,37.6810]]}`,
		},
	}
	p := &Parking{}
	bufs := p.BufferFeaturesPaired(context.Background(), features, testProj)
	if len(bufs) != 0 {
		t.Errorf("expected no buffered geometries when only LineString features (no polygons), got %d", len(bufs))
	}
}

func TestParking_BufferFeatures_Empty(t *testing.T) {
	p := &Parking{}
	bufs := p.BufferFeaturesPaired(context.Background(), nil, testProj)
	if len(bufs) != 0 {
		t.Errorf("expected no buffered geometries for empty features, got %d", len(bufs))
	}
}
