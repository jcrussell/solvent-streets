package hexmetric

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

func validSpec(key string) Spec {
	return Spec{Key: key, Label: "Test", Unit: "s", Source: "test", Format: FormatMinutes,
		Scale: Scale{Kind: KindSequential, Transform: TransformLinear, Bands: []float64{60, 120, 300}}}
}

func TestSpecValidate(t *testing.T) {
	if err := validSpec("test_metric_s").Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	bad := map[string]func(*Spec){
		"uppercase key":       func(s *Spec) { s.Key = "Fire" },
		"hyphen key":          func(s *Spec) { s.Key = "fire-response" },
		"leading digit":       func(s *Spec) { s.Key = "1x" },
		"double underscore":   func(s *Spec) { s.Key = "a__b" },
		"no label":            func(s *Spec) { s.Label = "" },
		"unknown format":      func(s *Spec) { s.Format = "hex" },
		"unknown kind":        func(s *Spec) { s.Scale.Kind = "rainbow" },
		"unknown transform":   func(s *Spec) { s.Scale.Transform = "sqrt" },
		"non-ascending bands": func(s *Spec) { s.Scale.Bands = []float64{60, 60} },
		"unknown mode":        func(s *Spec) { s.Scale.Mode = "smooth" },
		"ticks on bands":      func(s *Spec) { s.Scale.Ticks = []float64{60} },
		"one-stop ramp":       func(s *Spec) { s.Scale.Mode = ModeRamp; s.Scale.Bands = []float64{1} },
		"non-ascending ticks": func(s *Spec) { s.Scale.Mode = ModeRamp; s.Scale.Ticks = []float64{2, 1} },
		"unknown op":          func(s *Spec) { s.Derive = &Derivation{Op: "max", Prop: PropArea} },
		"unknown prop":        func(s *Spec) { s.Derive = &Derivation{Op: OpSum, Prop: "width"} },
		"sum with den":        func(s *Spec) { s.Derive = &Derivation{Op: OpSum, Prop: PropArea, Den: "population"} },
		"ratio without den":   func(s *Spec) { s.Derive = &Derivation{Op: OpRatio, Prop: PropArea} },
		"negative min_den":    func(s *Spec) { s.Derive = &Derivation{Op: OpRatio, Prop: PropArea, Den: "population", MinDen: -1} },
	}
	for name, mutate := range bad {
		s := validSpec("ok_key")
		mutate(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
	// A bandless scale is a single colour and an invalid paint expression.
	s := validSpec("one_band")
	s.Scale.Bands = nil
	if err := s.Validate(); err == nil {
		t.Error("bandless spec must be rejected")
	}
}

func TestRegisterLookupAll(t *testing.T) {
	if err := TryRegister(validSpec("zz_test_b")); err != nil {
		t.Fatal(err)
	}
	if err := TryRegister(validSpec("zz_test_a")); err != nil {
		t.Fatal(err)
	}
	if err := TryRegister(validSpec("zz_test_a")); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate key: got %v, want ErrDuplicate", err)
	}
	if err := TryRegister(Spec{Key: "Bad"}); err == nil {
		t.Error("invalid spec must not register")
	}
	if _, ok := Lookup("zz_test_a"); !ok {
		t.Error("Lookup failed for a registered key")
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
	if len(seen) != 2 || seen[0] != "zz_test_a" || seen[1] != "zz_test_b" {
		t.Errorf("All() order = %v, want sorted [zz_test_a zz_test_b]", seen)
	}
	defer func() {
		if recover() == nil {
			t.Error("Register must panic on a duplicate key")
		}
	}()
	Register(validSpec("zz_test_a"))
}

func TestSpecValidate_Derived(t *testing.T) {
	ramp := validSpec("ramp_pct")
	ramp.Format = FormatPercent
	ramp.Scale = Scale{Kind: KindCoverage, Transform: TransformLinear, Mode: ModeRamp, Bands: []float64{0, 100}, Ticks: []float64{0, 50, 100}}
	ramp.Derive = &Derivation{Op: OpSum, Prop: PropPct, Max: 100}
	if err := ramp.Validate(); err != nil {
		t.Errorf("valid ramp sum rejected: %v", err)
	}
	ratio := validSpec("road_m2_per_x")
	ratio.Derive = &Derivation{Op: OpRatio, Prop: PropArea, Den: "population", MinDen: 10}
	if err := ratio.Validate(); err != nil {
		t.Errorf("valid ratio rejected: %v", err)
	}
	if !ratio.Derived() || validSpec("x").Derived() {
		t.Error("Derived() must report exactly the specs with a Derivation")
	}
}

// TestBuiltins pins the pavement colorings as registered derived metrics,
// with the coverage ramp's stops and legend ticks the dashboard always had.
func TestBuiltins(t *testing.T) {
	for _, key := range []string{KeyCoverage, KeyArea, KeySpaces, KeyEstCost} {
		s, ok := Lookup(key)
		if !ok {
			t.Fatalf("built-in %s not registered", key)
		}
		if !s.Derived() || s.Derive.Op != OpSum {
			t.Errorf("%s must be a derived sum, got %+v", key, s.Derive)
		}
	}
	cov, _ := Lookup(KeyCoverage)
	if cov.Scale.Mode != ModeRamp || cov.Derive.Max != 100 {
		t.Errorf("coverage must be a ramp capped at 100: %+v", cov)
	}
	if got, want := cov.Scale.Bands, []float64{0, 10, 25, 50, 75, 100}; !slices.Equal(got, want) {
		t.Errorf("coverage stops = %v, want %v", got, want)
	}
	if got, want := cov.Scale.Ticks, []float64{0, 25, 50, 75, 100}; !slices.Equal(got, want) {
		t.Errorf("coverage ticks = %v, want %v", got, want)
	}
}

// TestScaleJSON_BandsUnchanged: Mode and Ticks are omitempty, so a bands
// scale serializes byte-identically to the v1 hex-metrics.json shape.
func TestScaleJSON_BandsUnchanged(t *testing.T) {
	b, err := json.Marshal(Scale{Kind: KindSequential, Transform: TransformLinear, Bands: []float64{1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"kind":"sequential","transform":"linear","bands":[1,2]}`; got != want {
		t.Errorf("scale JSON = %s, want %s", got, want)
	}
}
