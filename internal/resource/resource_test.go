package resource

import (
	"testing"
)

func TestAllContainsTwoTypes(t *testing.T) {
	if len(All) != 2 {
		t.Fatalf("expected 2 resource types, got %d", len(All))
	}
}

func TestByName_Pavements(t *testing.T) {
	rt := ByName("pavements")
	if rt == nil {
		t.Fatal("expected non-nil for pavements")
	}
	if _, ok := rt.(*Pavement); !ok {
		t.Errorf("expected *Pavement, got %T", rt)
	}
}

func TestByName_Unknown(t *testing.T) {
	rt := ByName("unknown")
	if rt != nil {
		t.Errorf("expected nil for unknown, got %v", rt)
	}
}
