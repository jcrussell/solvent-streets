package resource

import (
	"context"
	"strings"
	"testing"
)

func TestFacilityType(t *testing.T) {
	ft := FacilityType("fire")
	if ft != "facility-fire" || !ft.IsFacility() || ft.FacilityService() != "fire" {
		t.Errorf("FacilityType(fire) = %q (facility=%v service=%q)", ft, ft.IsFacility(), ft.FacilityService())
	}
	if TypeRoads.IsFacility() || TypeRoads.FacilityService() != "" {
		t.Error("roads must not read as a facility type")
	}
	// The hyphen keeps the scope machinery out of it: Bare() is the identity
	// and With(ScopeCity) does not panic on an "already scoped" colon.
	if ft.Bare() != ft || ft.Scope() != ScopeAll {
		t.Errorf("Bare()/Scope() on %q = %q/%q", ft, ft.Bare(), ft.Scope())
	}
	if ByType(ft) != nil {
		t.Error("facility types must not be in resource.All")
	}
}

func TestFacility_Source(t *testing.T) {
	f := &Facility{Service: "fire", Selectors: []string{`["amenity"="fire_station"]`}}
	if f.Type() != "facility-fire" || f.HasCohorts() || f.AsphaltSurfaced() {
		t.Errorf("unexpected Source flags: %+v", f)
	}
	q := f.OverpassQuery([4]float64{37.6, -121.8, 37.7, -121.7})
	for _, want := range []string{`node["amenity"="fire_station"](37.6`, `way["amenity"="fire_station"]`, `relation["amenity"="fire_station"]`, "out geom;"} {
		if !strings.Contains(q, want) {
			t.Errorf("query missing %q:\n%s", want, q)
		}
	}
	if got := f.BufferFeaturesPaired(context.Background(), []Feature{{ID: "x"}}, nil); got != nil {
		t.Errorf("facilities have no paved surface; got %v", got)
	}
}
