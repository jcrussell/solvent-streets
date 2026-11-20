package config

import (
	"strings"
	"testing"
)

// TestCostMultiplier_DefaultIsTheCalibratedOne: an unset cost_multiplier resolves to
// DefaultCostMultiplier. Pinned because the tiers LOOK ~2-3x low treatment-by-
// treatment (validation.md §3), which invites "helpfully" defaulting this to
// ~1.5 — but the system output is already loaded-equivalent (§6), so that would
// double-count and put Berkeley ~47% above its own cited figure.
func TestCostMultiplier_DefaultIsTheCalibratedOne(t *testing.T) {
	dir := t.TempDir()
	path := writeTOML(t, dir, "pvmt.toml", `
[[cities]]
name = "Alpha"
overpass = true
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	city := cityBySlug(t, cfg, "alpha")
	if got := cfg.ResolvedForecast(&city).CostMultiplier; got != DefaultCostMultiplier {
		t.Errorf("cost_multiplier = %v, want %v", got, DefaultCostMultiplier)
	}
}

// TestCostMultiplier_ExplicitOneIsHonored: an explicit per-city value must beat a
// top-level one in both directions. Unlike growth_rate this needs no presence
// bit — Validate rejects 0, so a positive test is an unambiguous presence check.
func TestCostMultiplier_ExplicitOneIsHonored(t *testing.T) {
	dir := t.TempDir()
	path := writeTOML(t, dir, "pvmt.toml", `
[forecast]
cost_multiplier = 1.8

[[cities]]
name = "Loaded Town"
overpass = true
[cities.forecast]
cost_multiplier = 1.0

[[cities]]
name = "Default Town"
overpass = true
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	loaded := cityBySlug(t, cfg, "loaded-town")
	if got := cfg.ResolvedForecast(&loaded).CostMultiplier; got != 1.0 {
		t.Errorf("loaded-town cost_multiplier = %v, want 1.0 (explicit per-city bare must beat the top level)", got)
	}
	deflt := cityBySlug(t, cfg, "default-town")
	if got := cfg.ResolvedForecast(&deflt).CostMultiplier; got != 1.8 {
		t.Errorf("default-town cost_multiplier = %v, want 1.8 from the top level", got)
	}
}

func TestCostMultiplier_ValidationRejectsNonsense(t *testing.T) {
	for _, tc := range []struct {
		name string
		val  string
	}{
		{"negative", "-1.0"},
		{"absurdly large", "50.0"},
		{"just past the cap", "5.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeTOML(t, dir, "pvmt.toml", `
[forecast]
cost_multiplier = `+tc.val+`

[[cities]]
name = "Alpha"
overpass = true
`)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("cost_multiplier = %s was accepted; want a validation error", tc.val)
			}
			if !strings.Contains(err.Error(), "cost_multiplier") {
				t.Errorf("error does not name the offending field: %v", err)
			}
		})
	}
}

// TestCostMultiplier_SurvivesIncludeFlatten: a per-city block whose ONLY field is
// cost_multiplier must not be judged "all zero" and erased by forecastIsZero at
// merge time — the same trap that dropped growth_rate = 0.
func TestCostMultiplier_SurvivesIncludeFlatten(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "metro/pvmt.toml", `
[[cities]]
name = "Loaded Town"
overpass = true
[cities.forecast]
cost_multiplier = 1.0
`)
	path := writeTOML(t, dir, "pvmt.toml", `
[[include]]
path = "metro/pvmt.toml"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	city := cityBySlug(t, cfg, "loaded-town")
	if city.Forecast == nil {
		t.Fatal("the per-city forecast block was erased at include-flatten time")
	}
	if got := cfg.ResolvedForecast(&city).CostMultiplier; got != 1.0 {
		t.Errorf("cost_multiplier = %v, want 1.0 through the include flatten", got)
	}
}

// TestCostMultiplier_NormalizeResolvesIt: the Config tab renders ResolvedTOML,
// which goes through NormalizeForecast rather than ResolvedForecast. If that
// path did not resolve the default, the tab would show `cost_multiplier = 0`
// while the forecast priced at DefaultCostMultiplier.
func TestCostMultiplier_NormalizeResolvesIt(t *testing.T) {
	fc := ForecastConfig{}
	NormalizeForecast(&fc)
	if fc.CostMultiplier != DefaultCostMultiplier {
		t.Errorf("NormalizeForecast left cost_multiplier = %v, want %v", fc.CostMultiplier, DefaultCostMultiplier)
	}

	explicit := ForecastConfig{CostMultiplier: 1.0}
	NormalizeForecast(&explicit)
	if explicit.CostMultiplier != 1.0 {
		t.Errorf("NormalizeForecast overwrote an explicit 1.0 with %v", explicit.CostMultiplier)
	}
}
