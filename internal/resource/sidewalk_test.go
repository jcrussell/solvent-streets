package resource

import (
	"context"
	"strings"
	"testing"
)

func TestSidewalk_Type(t *testing.T) {
	s := &Sidewalk{}
	if s.Type() != TypeSidewalks {
		t.Errorf("expected TypeSidewalks, got %v", s.Type())
	}
	if s.HasCohorts() {
		t.Error("expected HasCohorts to be false for sidewalks")
	}
}

func TestSidewalk_OverpassQuery(t *testing.T) {
	s := &Sidewalk{}
	q := s.OverpassQuery([4]float64{37.64, -121.84, 37.72, -121.68})
	if !strings.Contains(q, `"footway"="sidewalk"`) {
		t.Error("query should restrict footway=sidewalk")
	}
	for _, coord := range []string{"37.64", "-121.84", "37.72", "-121.68"} {
		if !strings.Contains(q, coord) {
			t.Errorf("query should contain bbox coord %s", coord)
		}
	}
}

func TestSidewalk_BufferFeatures_LineString(t *testing.T) {
	features := []Feature{
		{
			ID:           "sw1",
			Tags:         map[string]string{"footway": "sidewalk"},
			GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6800],[-121.7690,37.6810]]}`,
		},
	}
	s := &Sidewalk{}
	geoms := Geoms(s.BufferFeaturesPaired(context.Background(), features, testProj))
	if len(geoms) != 1 {
		t.Fatalf("expected 1 buffered geometry, got %d", len(geoms))
	}
	if geoms[0].Area() <= 0 {
		t.Errorf("expected positive buffered area, got %f", geoms[0].Area())
	}
}

func TestSidewalk_BufferFeatures_Empty(t *testing.T) {
	s := &Sidewalk{}
	bufs := s.BufferFeaturesPaired(context.Background(), nil, testProj)
	if len(bufs) != 0 {
		t.Errorf("expected no buffered geometries for empty feature slice, got %d", len(bufs))
	}
}
