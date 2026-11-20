package resource

import (
	"slices"
	"testing"
)

// TestByType: each pavement type resolves to its own Source, in the order
// the fan-out commands visit them.
func TestByType(t *testing.T) {
	want := []Type{TypeRoads, TypeParking, TypeSidewalks}
	if len(All) != len(want) {
		t.Fatalf("All has %d sources, want %d", len(All), len(want))
	}
	for i, rt := range want {
		if All[i].Type() != rt {
			t.Errorf("All[%d] is %q, want %q", i, All[i].Type(), rt)
		}
		if got := ByType(rt); got != All[i] {
			t.Errorf("ByType(%q) = %T, want All[%d]", rt, got, i)
		}
	}
}

func TestAllTypes_ScopeByScopeInAllOrder(t *testing.T) {
	got := AllTypes(ScopeAll, ScopeCity)
	want := []Type{TypeRoads, TypeParking, TypeSidewalks,
		TypeRoads.With(ScopeCity), TypeParking.With(ScopeCity), TypeSidewalks.With(ScopeCity)}
	if !slices.Equal(got, want) {
		t.Errorf("AllTypes(ScopeAll, ScopeCity) = %v, want %v", got, want)
	}
	if got := AllTypes(); len(got) != 0 {
		t.Errorf("AllTypes() = %v, want empty", got)
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

func TestPublicName(t *testing.T) {
	for in, want := range map[Type]string{
		TypeRoads:            "roads",
		TypeSidewalks:        "sidewalks",
		FacilityType("fire"): "fire_stations",
		TypeRoutingRoads:     "routing_roads",
		TypeCensusBlocks:     "census_blocks",
		Type("boundary"):     "boundary",
	} {
		if got := in.PublicName(); got != want {
			t.Errorf("%q.PublicName() = %q, want %q", in, got, want)
		}
	}
}
