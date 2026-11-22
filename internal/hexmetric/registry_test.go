package hexmetric

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/metric"
)

// Scratch catalog keys the tests present: durations, so validSpec's unit
// matches the catalog's.
func init() {
	for _, k := range []string{"test_metric_s", "ok_key_s", "zz_test_a_s", "zz_test_b_s", "one_band_s", "share_s", "x_s"} {
		metric.Register(metric.Def{Key: k, Unit: metric.UnitSeconds, Level: metric.Hex, Description: "test"})
	}
	metric.Register(metric.Def{Key: "zz_city_count", Unit: metric.UnitCount, Level: metric.City, Description: "test"})
}

func validSpec(key string) Spec {
	return Spec{Key: key, Label: "Test", Source: "test", Format: FormatMinutes, Group: GroupServices,
		Scale: Scale{Kind: KindSequential, Transform: TransformLinear, Bands: []float64{60, 120, 300}}}
}

func TestSpecValidate(t *testing.T) {
	if err := validSpec("test_metric_s").Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	bad := map[string]func(*Spec){
		"key not in catalog":  func(s *Spec) { s.Key = "fire-response" },
		"city-wide key":       func(s *Spec) { s.Key = "zz_city_count" },
		"city-wide derived":   func(s *Spec) { s.Key = "zz_city_count"; s.Derive = &Derivation{Op: OpSum, Prop: PropArea} },
		"unit off catalog":    func(s *Spec) { s.Unit = "m2" },
		"no label":            func(s *Spec) { s.Label = "" },
		"unknown group":       func(s *Spec) { s.Group = "misc" },
		"unknown format":      func(s *Spec) { s.Format = "hex" },
		"unknown kind":        func(s *Spec) { s.Scale.Kind = "rainbow" },
		"unknown transform":   func(s *Spec) { s.Scale.Transform = "sqrt" },
		"non-ascending bands": func(s *Spec) { s.Scale.Bands = []float64{60, 60} },
		"unknown op":          func(s *Spec) { s.Derive = &Derivation{Op: "max", Prop: PropArea} },
		"unknown prop":        func(s *Spec) { s.Derive = &Derivation{Op: OpSum, Prop: "width"} },
	}
	for name, mutate := range bad {
		s := validSpec("ok_key_s")
		mutate(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
	// A bandless scale is a single colour and an invalid paint expression.
	s := validSpec("one_band_s")
	s.Scale.Bands = nil
	if err := s.Validate(); err == nil {
		t.Error("bandless spec must be rejected")
	}
}

// unregisterOnCleanup removes keys from the registry when t ends, so a test
// that registers scratch keys leaves the shared registry as it found it and
// passes under -count=N.
func unregisterOnCleanup(t *testing.T, keys ...string) {
	t.Helper()
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, k := range keys {
			delete(registry, k)
		}
	})
}

func TestRegisterLookupAll(t *testing.T) {
	unregisterOnCleanup(t, "zz_test_a_s", "zz_test_b_s")
	if err := TryRegister(validSpec("zz_test_b_s")); err != nil {
		t.Fatal(err)
	}
	if err := TryRegister(validSpec("zz_test_a_s")); err != nil {
		t.Fatal(err)
	}
	if err := TryRegister(validSpec("zz_test_a_s")); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate key: got %v, want ErrDuplicate", err)
	}
	if err := TryRegister(Spec{Key: "Bad"}); err == nil {
		t.Error("invalid spec must not register")
	}
	if got, ok := Lookup("zz_test_a_s"); !ok {
		t.Error("Lookup failed for a registered key")
	} else if got.Unit != metric.UnitSeconds {
		t.Errorf("Register must take the catalog's unit, got %q", got.Unit)
	}
	if _, ok := Lookup("zz_missing"); ok {
		t.Error("Lookup succeeded for an unregistered key")
	}
	all := All()
	var seen []string
	for _, s := range all {
		if len(s.Key) > 7 && s.Key[:7] == "zz_test" {
			seen = append(seen, s.Key)
		}
	}
	if len(seen) != 2 || seen[0] != "zz_test_a_s" || seen[1] != "zz_test_b_s" {
		t.Errorf("All() order = %v, want sorted [zz_test_a zz_test_b]", seen)
	}
	defer func() {
		if recover() == nil {
			t.Error("Register must panic on a duplicate key")
		}
	}()
	Register(validSpec("zz_test_a_s"))
}

func TestSpecValidate_Derived(t *testing.T) {
	share := validSpec("share_s")
	share.Format = FormatPercent
	share.Derive = &Derivation{Op: OpSum, Prop: PropShare, Max: 1}
	if err := share.Validate(); err != nil {
		t.Errorf("valid capped sum rejected: %v", err)
	}
	if !share.Derived() || validSpec("x_s").Derived() {
		t.Error("Derived() must report exactly the specs with a Derivation")
	}
}

// TestBuiltins pins the pavement colorings as registered derived metrics,
// % Paved capped at a share of 1.
func TestBuiltins(t *testing.T) {
	for _, key := range []string{metric.MaintainedShare, metric.MaintainedArea, metric.ParkingSpaces, metric.Year1Need} {
		s, ok := Lookup(key)
		if !ok {
			t.Fatalf("built-in %s not registered", key)
		}
		if !s.Derived() || s.Derive.Op != OpSum {
			t.Errorf("%s must be a derived sum, got %+v", key, s.Derive)
		}
	}
	paved, _ := Lookup(metric.MaintainedShare)
	if paved.Derive.Max != 1 {
		t.Errorf("%% Paved must be capped at a share of 1: %+v", paved)
	}
}

// TestScaleJSON_BandsOnly: a scale serializes as kind, transform and bands
// only.
func TestScaleJSON_BandsOnly(t *testing.T) {
	b, err := json.Marshal(Scale{Kind: KindSequential, Transform: TransformLinear, Bands: []float64{1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"kind":"sequential","transform":"linear","bands":[1,2]}`; got != want {
		t.Errorf("scale JSON = %s, want %s", got, want)
	}
}
