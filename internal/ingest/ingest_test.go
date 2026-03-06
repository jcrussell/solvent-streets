package ingest

import (
	"testing"
)

func TestAllSources(t *testing.T) {
	sources := AllSources()
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(sources))
	}
}

func TestSourceByName_Overpass(t *testing.T) {
	src, err := SourceByName("overpass")
	if err != nil {
		t.Fatal(err)
	}
	if src.Name() != "overpass" {
		t.Errorf("expected overpass, got %s", src.Name())
	}
}

func TestSourceByName_Unknown(t *testing.T) {
	_, err := SourceByName("bogus")
	if err == nil {
		t.Error("expected error for unknown source")
	}
}
