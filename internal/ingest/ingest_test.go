package ingest

import (
	"testing"
)

var testBBox = [4]float64{37.64, -121.84, 37.72, -121.68}

func TestAllSources(t *testing.T) {
	sources := AllSources(testBBox, "")
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(sources))
	}
}

func TestSourceByName_Overpass(t *testing.T) {
	src, err := SourceByName("overpass", testBBox, "")
	if err != nil {
		t.Fatal(err)
	}
	if src.Name() != "overpass" {
		t.Errorf("expected overpass, got %s", src.Name())
	}
}

func TestSourceByName_Unknown(t *testing.T) {
	_, err := SourceByName("bogus", testBBox, "")
	if err == nil {
		t.Error("expected error for unknown source")
	}
}
