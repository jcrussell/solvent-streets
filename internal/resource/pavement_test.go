package resource

import (
	"strings"
	"testing"
)

func TestPavement_Name(t *testing.T) {
	p := &Pavement{}
	if p.Name() != "pavements" {
		t.Errorf("expected pavements, got %s", p.Name())
	}
}

func TestPavement_OverpassQuery(t *testing.T) {
	p := &Pavement{}
	q := p.OverpassQuery([4]float64{37.64, -121.84, 37.72, -121.68})
	if !strings.Contains(q, "highway") {
		t.Error("query should contain highway filter")
	}
	if !strings.Contains(q, "proposed") {
		t.Error("query should contain exclusion for proposed")
	}
	if !strings.Contains(q, "37.64") {
		t.Error("query should contain bbox south coordinate")
	}
}

func TestPavement_ProcessFeatures_LineString(t *testing.T) {
	// Use real Livermore-area coords so projection works
	features := []Feature{
		{
			ID:   "test1",
			Name: "Test Rd",
			Tags: map[string]string{"highway": "residential"},
			GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6800],[-121.7690,37.6810]]}`,
		},
	}
	p := &Pavement{}
	_, area, err := p.ProcessFeatures(features)
	if err != nil {
		t.Fatal(err)
	}
	if area <= 0 {
		t.Errorf("expected positive area, got %f", area)
	}
}

func TestPavement_ProcessFeatures_Polygon(t *testing.T) {
	features := []Feature{
		{
			ID:   "test2",
			Name: "Test Lot",
			Tags: map[string]string{},
			GeometryJSON: `{"type":"Polygon","coordinates":[[[-121.7700,37.6800],[-121.7690,37.6800],[-121.7690,37.6810],[-121.7700,37.6810],[-121.7700,37.6800]]]}`,
		},
	}
	p := &Pavement{}
	_, area, err := p.ProcessFeatures(features)
	if err != nil {
		t.Fatal(err)
	}
	if area <= 0 {
		t.Errorf("expected positive area, got %f", area)
	}
}

func TestPavement_ProcessFeatures_Empty(t *testing.T) {
	p := &Pavement{}
	_, _, err := p.ProcessFeatures(nil)
	if err == nil {
		t.Error("expected error for empty features")
	}
}

func TestPavement_ProcessFeatures_InvalidSkipped(t *testing.T) {
	features := []Feature{
		{
			ID:           "bad",
			GeometryJSON: `not json`,
		},
		{
			ID:   "good",
			Name: "Good Rd",
			Tags: map[string]string{"highway": "residential"},
			GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6800],[-121.7690,37.6810]]}`,
		},
	}
	p := &Pavement{}
	_, area, err := p.ProcessFeatures(features)
	if err != nil {
		t.Fatal(err)
	}
	if area <= 0 {
		t.Errorf("expected positive area from valid feature, got %f", area)
	}
}
