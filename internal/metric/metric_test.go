package metric

import (
	"errors"
	"testing"
)

// TestCatalog_Valid: every declared key passes the naming rule (Register
// panics otherwise, so reaching here means init ran) and is found by Lookup.
func TestCatalog_Valid(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("the catalog is empty")
	}
	for _, d := range all {
		if err := d.Validate(); err != nil {
			t.Error(err)
		}
		if got, ok := Lookup(d.Key); !ok || got != d {
			t.Errorf("Lookup(%q) = %+v, %v", d.Key, got, ok)
		}
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
	}
	for name, d := range bad {
		if err := d.Validate(); err == nil {
			t.Errorf("%s: %s accepted", name, d.Key)
		}
	}
}

func TestRegister_Duplicate(t *testing.T) {
	d := Def{"zz_test_metric", UnitCount, Hex, "test"}
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
