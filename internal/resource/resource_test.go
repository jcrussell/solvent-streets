package resource

import (
	"testing"
)

func TestAllContainsThreeTypes(t *testing.T) {
	if len(All) != 3 {
		t.Fatalf("expected 3 resource types, got %d", len(All))
	}
}

func TestByType_Pavement(t *testing.T) {
	rt := ByType(TypeRoads)
	if rt == nil {
		t.Fatal("expected non-nil for TypeRoads")
	}
	if _, ok := rt.(*Pavement); !ok {
		t.Errorf("expected *Pavement, got %T", rt)
	}
}

func TestByType_Unknown(t *testing.T) {
	if rt := ByType("nonexistent"); rt != nil {
		t.Errorf("expected nil for unknown type, got %v", rt)
	}
}

func TestType_WithAndBare(t *testing.T) {
	city := TypeRoads.With(ScopeCity)
	if city != "roads:city" {
		t.Errorf("TypeRoads.With(ScopeCity) = %q; want roads:city", city)
	}
	if city.Bare() != TypeRoads {
		t.Errorf("city.Bare() = %q; want %q", city.Bare(), TypeRoads)
	}
	if city.Scope() != ScopeCity {
		t.Errorf("city.Scope() = %q; want %q", city.Scope(), ScopeCity)
	}
	if TypeRoads.Scope() != ScopeAll {
		t.Errorf("bare TypeRoads.Scope() = %q; want ScopeAll", TypeRoads.Scope())
	}
}
