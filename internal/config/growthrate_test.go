package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// growthRateOf resolves a city's effective growth rate the way the forecast
// path does, so these tests assert on what a forecast would actually simulate
// rather than on the config plumbing that produces it.
func growthRateOf(t *testing.T, cfg *Config, slug string) float64 {
	t.Helper()
	city := cityBySlug(t, cfg, slug)
	fc := cfg.ResolvedForecast(&city)
	return fc.GrowthRate
}

// TestGrowthRate_ExplicitPerCityZeroOverridesTopLevel is solvent-streets-r312.
//
// growth_rate is the only forecast field where 0 is a value rather than an
// unset sentinel: "this city's network does not grow" is a real statement, and
// it has to beat a positive top-level rate. applyCityForecastProv used to guard
// on `ov.GrowthRate != 0`, so the explicit zero was indistinguishable from an
// absent field and the city silently inherited 0.02.
func TestGrowthRate_ExplicitPerCityZeroOverridesTopLevel(t *testing.T) {
	dir := t.TempDir()
	path := writeTOML(t, dir, "pvmt.toml", `
[forecast]
growth_rate = 0.02

[[cities]]
name = "Flat Town"
overpass = true
[cities.forecast]
growth_rate = 0.0

[[cities]]
name = "Growing Town"
overpass = true

[[cities]]
name = "Shrinking Town"
overpass = true
[cities.forecast]
growth_rate = -0.01
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := growthRateOf(t, cfg, "flat-town"); got != 0 {
		t.Errorf("flat-town growth_rate = %v, want 0 (explicit per-city zero must override the top level)", got)
	}
	if got := growthRateOf(t, cfg, "growing-town"); got != 0.02 {
		t.Errorf("growing-town growth_rate = %v, want 0.02 (no override, inherits the top level)", got)
	}
	if got := growthRateOf(t, cfg, "shrinking-town"); got != -0.01 {
		t.Errorf("shrinking-town growth_rate = %v, want -0.01", got)
	}
}

// TestGrowthRate_ExplicitZeroSurvivesIncludeFlatten covers the second half of
// r312, which is where the value was actually being erased. mergeIncludedCities
// flattens each child city's calibration through effectiveForecast, and
// forecastIsZero discarded any block whose fields were all zero — so a city
// whose only override was `growth_rate = 0` had that override dropped at merge
// time and inherited the PARENT's rate instead.
func TestGrowthRate_ExplicitZeroSurvivesIncludeFlatten(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "metro/pvmt.toml", `
[forecast]
growth_rate = 0.03

[[cities]]
name = "Flat Town"
overpass = true
[cities.forecast]
growth_rate = 0.0

[[cities]]
name = "Metro Town"
overpass = true
`)
	path := writeTOML(t, dir, "pvmt.toml", `
[[include]]
path = "metro/pvmt.toml"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := growthRateOf(t, cfg, "flat-town"); got != 0 {
		t.Errorf("flat-town growth_rate = %v, want 0 through the include flatten", got)
	}
	// The sibling proves the child's top-level rate still flattens normally, so
	// the test above is not passing just because nothing propagated at all.
	if got := growthRateOf(t, cfg, "metro-town"); got != 0.03 {
		t.Errorf("metro-town growth_rate = %v, want 0.03 flattened from the child's [forecast]", got)
	}
}

// TestGrowthRate_ExplicitZeroBeatsLaterIncludeBackfill: the per-field union
// backfills a field the winner left unset. An explicit zero is SET, not unset,
// so a later include's positive rate must be reported as a conflict and
// discarded — not silently backfilled over it. Plain unionFloat could not
// express this, since it reads *dst == 0 as "unset".
func TestGrowthRate_ExplicitZeroBeatsLaterIncludeBackfill(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "flat/pvmt.toml", `
[[cities]]
name = "Shared Town"
overpass = true
[cities.forecast]
growth_rate = 0.0
`)
	writeTOML(t, dir, "growth/pvmt.toml", `
[[cities]]
name = "Shared Town"
overpass = true
[cities.forecast]
growth_rate = 0.05
`)
	path := writeTOML(t, dir, "pvmt.toml", `
[[include]]
path = "flat/pvmt.toml"
[[include]]
path = "growth/pvmt.toml"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := growthRateOf(t, cfg, "shared-town"); got != 0 {
		t.Errorf("shared-town growth_rate = %v, want 0 — the first include stated it and wins", got)
	}
	// Something WAS discarded, so the merge must say so rather than swallow it.
	warnings := strings.Join(cfg.LoadWarnings(), "\n")
	if !strings.Contains(warnings, "growth_rate") {
		t.Errorf("no growth_rate conflict warning; a discarded value must be reported.\ngot:\n%s", warnings)
	}
}

// TestGrowthRate_UnsetStillBackfillsFromLaterInclude is the other direction:
// presence, not value, is what blocks a backfill. A city that never stated
// growth_rate must still pick one up from a later include, exactly as before —
// otherwise the fix would turn every absent field into a permanent hole.
func TestGrowthRate_UnsetStillBackfillsFromLaterInclude(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "bare/pvmt.toml", `
[[cities]]
name = "Shared Town"
overpass = true
[cities.forecast]
initial_pci = 70
`)
	writeTOML(t, dir, "growth/pvmt.toml", `
[[cities]]
name = "Shared Town"
overpass = true
[cities.forecast]
growth_rate = 0.05
`)
	path := writeTOML(t, dir, "pvmt.toml", `
[[include]]
path = "bare/pvmt.toml"
[[include]]
path = "growth/pvmt.toml"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := growthRateOf(t, cfg, "shared-town"); got != 0.05 {
		t.Errorf("shared-town growth_rate = %v, want 0.05 backfilled from the second include", got)
	}
	if w := strings.Join(cfg.LoadWarnings(), "\n"); strings.Contains(w, "growth_rate") {
		t.Errorf("backfill warned; only a genuine disagreement should.\ngot:\n%s", w)
	}
}

// TestGrowthRate_PresenceIsPerCityNotPerFile pins the reason presence comes
// from a shadow decode rather than BurntSushi's MetaData: IsDefined cannot
// address an individual [[cities]] element, and Keys() flattens every element
// into one list. A per-file bit would mark EVERY city as having stated
// growth_rate the moment any one of them did.
func TestGrowthRate_PresenceIsPerCityNotPerFile(t *testing.T) {
	dir := t.TempDir()
	path := writeTOML(t, dir, "pvmt.toml", `
[forecast]
growth_rate = 0.02

[[cities]]
name = "Stater"
overpass = true
[cities.forecast]
growth_rate = 0.0

[[cities]]
name = "Quiet"
overpass = true
[cities.forecast]
initial_pci = 70
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	stater := cityBySlug(t, cfg, "stater")
	quiet := cityBySlug(t, cfg, "quiet")
	if !stater.Forecast.GrowthRateSet() {
		t.Error("stater: growth_rate presence not recorded")
	}
	if quiet.Forecast.GrowthRateSet() {
		t.Error("quiet: growth_rate marked present, but this city never stated it")
	}
	if got := growthRateOf(t, cfg, "quiet"); got != 0.02 {
		t.Errorf("quiet growth_rate = %v, want 0.02 — a neighbour's explicit zero must not leak", got)
	}
}

// TestGrowthRate_PresenceDoesNotMoveTheConfigHash: growthRateSet is unexported
// precisely so it stays out of Config.Hash's struct-encoding fallback. If it
// leaked in, adding the field would orphan every stored snapshot for
// in-memory configs.
func TestGrowthRate_PresenceDoesNotMoveTheConfigHash(t *testing.T) {
	a := &Config{Forecast: ForecastConfig{GrowthRate: 0}}
	b := &Config{Forecast: ForecastConfig{GrowthRate: 0, growthRateSet: true}}
	if a.Hash() != b.Hash() {
		t.Errorf("presence bit changed the hash: %s vs %s", a.Hash(), b.Hash())
	}
}

// TestGrowthRate_ProvenanceLabelsExplicitZero: `config show --sources` must
// attribute an explicit zero to the city that stated it. Before the fix the
// label was dropped along with the value.
func TestGrowthRate_ProvenanceLabelsExplicitZero(t *testing.T) {
	dir := t.TempDir()
	path := writeTOML(t, dir, "pvmt.toml", `
[forecast]
growth_rate = 0.02

[[cities]]
name = "Flat Town"
overpass = true
[cities.forecast]
growth_rate = 0.0
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	city := cityBySlug(t, cfg, "flat-town")

	var fc ForecastConfig
	var prov forecastProvenance
	fc = cfg.Forecast
	applyCityForecastProv(&fc, &prov, &city)

	if fc.GrowthRate != 0 {
		t.Errorf("resolved growth_rate = %v, want 0", fc.GrowthRate)
	}
	want := filepath.Base("cities[flat-town].forecast.growth_rate")
	if prov.GrowthRate.Detail != want {
		t.Errorf("provenance detail = %q, want %q", prov.GrowthRate.Detail, want)
	}
	if prov.GrowthRate.Kind != SourceFile {
		t.Errorf("provenance kind = %v, want SourceFile", prov.GrowthRate.Kind)
	}
}

// TestGrowthRate_ZeroInIncludingParentIsRejected closes a hole in the
// [[include]] calibration gate. That gate (Config.validate) rejects a file that
// both declares [[include]] and states its own top-level [forecast], because
// the merge would silently re-calibrate every included city. It asks
// forecastIsZero, which only sees an explicit `growth_rate = 0` through the
// presence bit — and parseConfig used to set that bit AFTER validating, so the
// bit was always false at gate time.
//
// The root config was still caught by the post-merge Validate, but an
// INTERMEDIATE parent (included by the root, and itself including a child) is
// only ever checked per-file, so it slipped through and its zero went on to
// flatten over its children's rates.
func TestGrowthRate_ZeroInIncludingParentIsRejected(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "child/pvmt.toml", `
[forecast]
growth_rate = 0.03

[[cities]]
name = "Leaf Town"
overpass = true
`)
	// The intermediate: it includes the child AND states its own calibration.
	writeTOML(t, dir, "mid/pvmt.toml", `
[forecast]
growth_rate = 0.0

[[include]]
path = "../child/pvmt.toml"
`)
	path := writeTOML(t, dir, "pvmt.toml", `
[[include]]
path = "mid/pvmt.toml"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load succeeded; want the transitive parent's [forecast] growth_rate = 0 rejected by the include gate")
	}
	if !strings.Contains(err.Error(), "[[include]]") {
		t.Errorf("Load error = %v, want the include-calibration gate message", err)
	}
}

// TestGrowthRate_ZeroInLeafConfigStillLoads is the control for the test above:
// moving applyForecastPresence ahead of validate must not make a plain
// `growth_rate = 0` illegal in a config that has no [[include]] at all.
func TestGrowthRate_ZeroInLeafConfigStillLoads(t *testing.T) {
	dir := t.TempDir()
	path := writeTOML(t, dir, "pvmt.toml", `
[forecast]
growth_rate = 0.0

[[cities]]
name = "Flat Town"
overpass = true
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := growthRateOf(t, cfg, "flat-town"); got != 0 {
		t.Errorf("flat-town growth_rate = %v, want 0", got)
	}
}
