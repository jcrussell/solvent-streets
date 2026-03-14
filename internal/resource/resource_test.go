package resource

import (
	"testing"
)

func TestAllContainsThreeTypes(t *testing.T) {
	if len(All) != 3 {
		t.Fatalf("expected 3 resource types, got %d", len(All))
	}
}

func TestByName_Pavements(t *testing.T) {
	rt := ByName("roads")
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
