package metric

import (
	"errors"
	"testing"
)

// unregisterOnCleanup removes keys from the catalog when t ends, so a test
// that declares scratch keys leaves the shared catalog as it found it and
// passes under -count=N.
func unregisterOnCleanup(t *testing.T, keys ...string) {
	t.Helper()
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, k := range keys {
			delete(catalog, k)
		}
	})
}

// TestCatalog_LookupRoundTrip: the catalog is populated and Lookup finds what
// All lists.
func TestCatalog_LookupRoundTrip(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("the catalog is empty")
	}
	if got, ok := Lookup(all[0].Key); !ok || got != all[0] {
		t.Errorf("Lookup(%q) = %+v, %v; want %+v", all[0].Key, got, ok, all[0])
	}
}

func TestDefValidate_NamingRule(t *testing.T) {
	ok := []Def{
		{"paved_area", UnitSquareMeters, Both, "x"},
		{"min_pci", UnitCount, Hex, "x"},
		{"wait_s", UnitSeconds, Hex, "x"},
		{"tree_share", UnitShare, Hex, "x"},
		{"cost_per_resident", UnitDollarsPerResident, Derived, "x"},
		{"population_density", UnitPerSquareMeter, Hex, "x"},
		{"cost_per_sqm", "usd/m2", Hex, "x"},
	}
	for _, d := range ok {
		if err := d.Validate(); err != nil {
			t.Errorf("%s rejected: %v", d.Key, err)
		}
	}
	bad := map[string]Def{
		"uppercase":            {"Paved", UnitCount, City, "x"},
		"no level":             {"paved", UnitCount, 0, "x"},
		"no unit":              {"paved", "", City, "x"},
		"undeclared unit":      {"paved", "acres", City, "x"},
		"spelled-out unit":     {"response_minutes", UnitCount, Hex, "x"},
		"no description":       {"paved", UnitCount, City, ""},
		"area unit suffix":     {"paved_area_m2", UnitSquareMeters, Both, "x"},
		"money unit suffix":    {"est_cost_usd", UnitDollars, Derived, "x"},
		"percent":              {"coverage_pct", UnitShare, Hex, "x"},
		"unit inside a rate":   {"road_m2_per_resident", UnitSquareMetersPerResident, Derived, "x"},
		"density per km2":      {"population_per_km2", UnitPerSquareMeter, Hex, "x"},
		"duration without _s":  {"fire_response", UnitSeconds, Hex, "x"},
		"_s without duration":  {"things_s", UnitCount, Hex, "x"},
		"share without _share": {"coverage", UnitShare, Hex, "x"},
		"_share not 0..1":      {"coverage_share", UnitCount, Hex, "x"},
		"rate without a rate":  {"cars_per_hex", UnitCount, Hex, "x"},
		"sqm outside a rate":   {"paved_sqm", UnitSquareMeters, Hex, "x"},
	}
	for name, d := range bad {
		if err := d.Validate(); err == nil {
			t.Errorf("%s: %s accepted", name, d.Key)
		}
	}
}

func TestRegister_Duplicate(t *testing.T) {
	d := Def{"zz_test_metric", UnitCount, Hex, "test"}
	unregisterOnCleanup(t, d.Key)
	if err := TryRegister(d); err != nil {
		t.Fatal(err)
	}
	if err := TryRegister(d); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate: got %v, want ErrDuplicate", err)
	}
}

func TestCheckLevel(t *testing.T) {
	for _, tc := range []struct {
		key       string
		city, hex bool
	}{
		{PavedArea, true, true},
		{PavedShare, false, true},
		{FeatureCount, true, false},
		{TurnoutS, true, false},
		{Year1Need, false, false},
		{"nope", false, false},
	} {
		if got := CheckCity(tc.key) == nil; got != tc.city {
			t.Errorf("CheckCity(%q) ok = %v, want %v", tc.key, got, tc.city)
		}
		if got := CheckHex(tc.key) == nil; got != tc.hex {
			t.Errorf("CheckHex(%q) ok = %v, want %v", tc.key, got, tc.hex)
		}
	}
}
