package hexmetric

import (
	"errors"
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
	}
	for name, mutate := range bad {
		s := validSpec("ok_key")
		mutate(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
	// No bands is legal (one band).
	s := validSpec("one_band")
	s.Scale.Bands = nil
	if err := s.Validate(); err != nil {
		t.Errorf("bandless spec rejected: %v", err)
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
