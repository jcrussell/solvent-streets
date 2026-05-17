package resource

import (
	"testing"
)

func TestAllContainsThreeTypes(t *testing.T) {
	if len(All) != 3 {
		t.Fatalf("expected 3 resource types, got %d", len(All))
	}
}

func TestByKind_Pavements(t *testing.T) {
	rt := ByKind(KindRoads)
	if rt == nil {
		t.Fatal("expected non-nil for pavements")
	}
	if _, ok := rt.(*Pavement); !ok {
		t.Errorf("expected *Pavement, got %T", rt)
	}
}

func TestByKind_Unknown(t *testing.T) {
	if rt := ByKind(KindUnknown); rt != nil {
		t.Errorf("expected nil for KindUnknown, got %v", rt)
	}
}
