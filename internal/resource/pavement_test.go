package resource

import (
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/geo"
)

var testProj = geo.NewUTMProjector(-121.76, 37.68)

func TestPavement_Name(t *testing.T) {
	p := &Pavement{}
	if p.Name() != "roads" {
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

func TestPavement_BufferFeatures_LineString(t *testing.T) {
	features := []Feature{
		{
			ID:           "test1",
			Name:         "Test Rd",
			Tags:         map[string]string{"highway": "residential"},
			GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6800],[-121.7690,37.6810]]}`,
		},
	}
	p := &Pavement{}
	geoms, err := p.BufferFeatures(features, testProj)
	if err != nil {
		t.Fatal(err)
	}
	if len(geoms) != 1 {
		t.Fatalf("expected 1 buffered geometry, got %d", len(geoms))
	}
	if geoms[0].Area() <= 0 {
		t.Errorf("expected positive area, got %f", geoms[0].Area())
	}
}

func TestPavement_BufferFeatures_Polygon(t *testing.T) {
	features := []Feature{
		{
			ID:           "test2",
			Name:         "Test Lot",
			Tags:         map[string]string{},
			GeometryJSON: `{"type":"Polygon","coordinates":[[[-121.7700,37.6800],[-121.7690,37.6800],[-121.7690,37.6810],[-121.7700,37.6810],[-121.7700,37.6800]]]}`,
		},
	}
	p := &Pavement{}
	geoms, err := p.BufferFeatures(features, testProj)
	if err != nil {
		t.Fatal(err)
	}
	if len(geoms) != 1 {
		t.Fatalf("expected 1 buffered geometry, got %d", len(geoms))
	}
	if geoms[0].Area() <= 0 {
		t.Errorf("expected positive area, got %f", geoms[0].Area())
	}
}

func TestPavement_BufferFeatures_Empty(t *testing.T) {
	p := &Pavement{}
	_, err := p.BufferFeatures(nil, testProj)
	if err == nil {
		t.Error("expected error for empty features")
	}
}

func TestPavement_BufferFeatures_InvalidSkipped(t *testing.T) {
	features := []Feature{
		{
			ID:           "bad",
			GeometryJSON: `not json`,
		},
		{
			ID:           "good",
			Name:         "Good Rd",
			Tags:         map[string]string{"highway": "residential"},
			GeometryJSON: `{"type":"LineString","coordinates":[[-121.7700,37.6800],[-121.7690,37.6810]]}`,
		},
	}
	p := &Pavement{}
	geoms, err := p.BufferFeatures(features, testProj)
	if err != nil {
		t.Fatal(err)
	}
	if len(geoms) != 1 {
		t.Errorf("expected 1 valid buffered geometry, got %d", len(geoms))
	}
}
