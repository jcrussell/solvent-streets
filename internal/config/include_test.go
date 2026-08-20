package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTOML writes content to dir/name and returns the full path.
func writeTOML(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func cityBySlug(t *testing.T, cfg *Config, slug string) CityConfig {
	t.Helper()
	for _, c := range cfg.Cities {
		if c.Slug() == slug {
			return c
		}
	}
	t.Fatalf("city %q not found in merged config (have %d cities)", slug, len(cfg.Cities))
	return CityConfig{}
}

// TestInclude_MergesAndUnionsTags: a city pulled in by two includes appears
// once with the union of both includes' tags; distinct cities are appended.
// The shared city (San Jose) carries identical calibration in both includes so
// the merge is a clean union (a *differing* calibration keeps the first
// include's values and warns — see TestInclude_ConflictingCalibration).
func TestInclude_MergesAndUnionsTags(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "bay/pvmt.toml", `
[forecast]
growth_rate = 0.01
[[cities]]
name = "San Jose, CA"
overpass = true
[[cities]]
name = "Oakland, CA"
overpass = true
`)
	writeTOML(t, dir, "top/pvmt.toml", `
[forecast]
growth_rate = 0.01
[[cities]]
name = "San Jose, CA"
overpass = true
[[cities]]
name = "Austin, TX"
overpass = true
`)
	top := writeTOML(t, dir, "all/pvmt.toml", `
[[include]]
path = "../bay/pvmt.toml"
tags = ["Bay Area"]
[[include]]
path = "../top/pvmt.toml"
tags = ["Top 50"]
`)

	cfg, err := Load(top)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Cities) != 3 {
		t.Fatalf("expected 3 unique cities (San Jose, Oakland, Austin), got %d: %+v", len(cfg.Cities), cfg.Cities)
	}
	sj := cityBySlug(t, cfg, "san-jose-ca")
	if !equalStrings(sj.Tags, []string{"Bay Area", "Top 50"}) {
		t.Errorf("San Jose tags = %v; want [Bay Area Top 50]", sj.Tags)
	}
	oak := cityBySlug(t, cfg, "oakland-ca")
	if !equalStrings(oak.Tags, []string{"Bay Area"}) {
		t.Errorf("Oakland tags = %v; want [Bay Area]", oak.Tags)
	}
}

// TestInclude_PreservesPerMetroCalibration: with an empty top-level forecast,
// each merged city still resolves to its source example's flattened
// calibration, and a per-city override survives the flatten. Two distinct
// metros are included (no shared city), so there is no cross-include collision.
func TestInclude_PreservesPerMetroCalibration(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "bay/pvmt.toml", `
[forecast]
growth_rate = 0.005
decay_rate = 0.04
[[cities]]
name = "San Jose, CA"
overpass = true
[[cities]]
name = "Oakland, CA"
overpass = true
forecast.decay_rate = 0.07
`)
	writeTOML(t, dir, "boston/pvmt.toml", `
[forecast]
growth_rate = 0.015
decay_rate = 0.065
[[cities]]
name = "Boston, MA"
overpass = true
`)
	top := writeTOML(t, dir, "all/pvmt.toml", `
[[include]]
path = "../bay/pvmt.toml"
tags = ["Bay Area"]
[[include]]
path = "../boston/pvmt.toml"
tags = ["Boston"]
`)

	cfg, err := Load(top)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Top-level forecast is empty, so cities keep their flattened calibration.
	sj := cityBySlug(t, cfg, "san-jose-ca")
	fc := cfg.ResolvedForecast(&sj)
	if fc.GrowthRate != 0.005 || fc.DecayRate != 0.04 {
		t.Errorf("San Jose resolved forecast = growth %g decay %g; want growth 0.005 decay 0.04 (bay flatten)", fc.GrowthRate, fc.DecayRate)
	}
	// Boston keeps its own metro's calibration.
	bos := cityBySlug(t, cfg, "boston-ma")
	bfc := cfg.ResolvedForecast(&bos)
	if bfc.GrowthRate != 0.015 || bfc.DecayRate != 0.065 {
		t.Errorf("Boston resolved forecast = growth %g decay %g; want growth 0.015 decay 0.065", bfc.GrowthRate, bfc.DecayRate)
	}
	// Oakland's per-city decay override survives the flatten.
	oak := cityBySlug(t, cfg, "oakland-ca")
	ofc := cfg.ResolvedForecast(&oak)
	if ofc.DecayRate != 0.07 || ofc.GrowthRate != 0.005 {
		t.Errorf("Oakland resolved forecast = growth %g decay %g; want growth 0.005 decay 0.07", ofc.GrowthRate, ofc.DecayRate)
	}
}

// TestInclude_ConflictingCalibration: the same city reached through two
// includes with *different* non-empty calibration keeps the FIRST include's
// values (vf6m — the behavior examples/all's header has always documented)
// and records a warning rather than hard-erroring. This inverts the original
// 3vhw expectation: a conflict used to abort the load, which made examples/all
// — the config `make site` builds — unloadable. Identical or empty calibration
// on the second include is not a disagreement and stays silent (subtests
// below and TestInclude_ConflictingMinHexArea).
func TestInclude_ConflictingCalibration(t *testing.T) {
	t.Run("forecast first include wins", func(t *testing.T) {
		dir := t.TempDir()
		writeTOML(t, dir, "bay/pvmt.toml", `
[forecast]
decay_rate = 0.04
[[cities]]
name = "San Jose, CA"
overpass = true
`)
		writeTOML(t, dir, "top/pvmt.toml", `
[forecast]
decay_rate = 0.065
[[cities]]
name = "San Jose, CA"
overpass = true
`)
		top := writeTOML(t, dir, "all/pvmt.toml", `
[[include]]
path = "../bay/pvmt.toml"
[[include]]
path = "../top/pvmt.toml"
`)
		cfg, err := Load(top)
		if err != nil {
			t.Fatalf("conflicting forecast should load with a warning, got %v", err)
		}
		sj := cityBySlug(t, cfg, "san-jose-ca")
		if got := cfg.ResolvedForecast(&sj).DecayRate; got != 0.04 {
			t.Errorf("San Jose decay = %g; want 0.04 (first include wins)", got)
		}
		w := onlyWarning(t, cfg)
		for _, want := range []string{"San Jose, CA", "san-jose-ca", "forecast.decay_rate (0.04 vs 0.065)", "../bay/pvmt.toml", "../top/pvmt.toml"} {
			if !strings.Contains(w, want) {
				t.Errorf("warning %q missing %q", w, want)
			}
		}
	})

	t.Run("hex_edge first include wins", func(t *testing.T) {
		dir := t.TempDir()
		writeTOML(t, dir, "a/pvmt.toml", `
[grid]
hex_edge_m = 80
[[cities]]
name = "Reno, NV"
overpass = true
`)
		writeTOML(t, dir, "b/pvmt.toml", `
[grid]
hex_edge_m = 55
[[cities]]
name = "Reno, NV"
overpass = true
`)
		top := writeTOML(t, dir, "all/pvmt.toml", `
[[include]]
path = "../a/pvmt.toml"
[[include]]
path = "../b/pvmt.toml"
`)
		cfg, err := Load(top)
		if err != nil {
			t.Fatalf("conflicting hex_edge_m should load with a warning, got %v", err)
		}
		reno := cityBySlug(t, cfg, "reno-nv")
		if got := cfg.ResolvedHexEdge(&reno); got != 80 {
			t.Errorf("Reno hex edge = %g; want 80 (first include wins)", got)
		}
		if w := onlyWarning(t, cfg); !strings.Contains(w, "hex_edge_m (80 vs 55)") {
			t.Errorf("warning %q should name the superseded hex_edge_m values", w)
		}
	})

	t.Run("second include empty calibration is fine", func(t *testing.T) {
		dir := t.TempDir()
		writeTOML(t, dir, "a/pvmt.toml", `
[grid]
hex_edge_m = 80
[forecast]
decay_rate = 0.04
[[cities]]
name = "Reno, NV"
overpass = true
`)
		// b contributes the same city with no calibration at all: defers to a's.
		writeTOML(t, dir, "b/pvmt.toml", `
[[cities]]
name = "Reno, NV"
overpass = true
`)
		top := writeTOML(t, dir, "all/pvmt.toml", `
[[include]]
path = "../a/pvmt.toml"
tags = ["A"]
[[include]]
path = "../b/pvmt.toml"
tags = ["B"]
`)
		cfg, err := Load(top)
		if err != nil {
			t.Fatalf("empty second calibration should merge cleanly, got %v", err)
		}
		reno := cityBySlug(t, cfg, "reno-nv")
		if got := cfg.ResolvedHexEdge(&reno); got != 80 {
			t.Errorf("Reno hex edge = %g; want 80 (first include's value kept)", got)
		}
		if rfc := cfg.ResolvedForecast(&reno); rfc.DecayRate != 0.04 {
			t.Errorf("Reno decay = %g; want 0.04 (first include's value kept)", rfc.DecayRate)
		}
		if !equalStrings(reno.Tags, []string{"A", "B"}) {
			t.Errorf("Reno tags = %v; want [A B]", reno.Tags)
		}
		// Deferral is not a disagreement: nothing was dropped, so warning
		// about it would be pure noise on a config like examples/all where
		// most shared cities take this path.
		if w := cfg.LoadWarnings(); len(w) != 0 {
			t.Errorf("an unset second calibration must not warn, got %q", w)
		}
	})
}

// onlyWarning asserts cfg collected exactly one load warning and returns it.
func onlyWarning(t *testing.T, cfg *Config) string {
	t.Helper()
	w := cfg.LoadWarnings()
	if len(w) != 1 {
		t.Fatalf("want exactly 1 load warning, got %d: %q", len(w), w)
	}
	return w[0]
}

// TestInclude_IdenticalCalibrationIsSilent: two includes that agree are not a
// disagreement and must not warn. This is the noise guard — examples/all
// unions ten files and most of its cities appear in exactly one, so a warning
// per shared city would train the reader to ignore the 13 real ones.
func TestInclude_IdenticalCalibrationIsSilent(t *testing.T) {
	dir := t.TempDir()
	body := `
[grid]
hex_edge_m = 80
[display]
min_hex_area = 20
[forecast]
decay_rate = 0.04
years = 25
[[cities]]
name = "Reno, NV"
overpass = true
`
	writeTOML(t, dir, "a/pvmt.toml", body)
	writeTOML(t, dir, "b/pvmt.toml", body)
	top := writeTOML(t, dir, "all/pvmt.toml", `
[[include]]
path = "../a/pvmt.toml"
[[include]]
path = "../b/pvmt.toml"
`)
	cfg, err := Load(top)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if w := cfg.LoadWarnings(); len(w) != 0 {
		t.Errorf("identical calibration must not warn, got %q", w)
	}
}

// TestInclude_ParentDeclaredCityWins: a city the parent declares directly is
// exempt from first-include-wins bookkeeping — "the parent's own city wins" is
// a documented local override, not a peer conflict, so it stays silent.
func TestInclude_ParentDeclaredCityWins(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "a/pvmt.toml", `
[grid]
hex_edge_m = 55
[[cities]]
name = "Reno, NV"
overpass = true
`)
	top := writeTOML(t, dir, "all/pvmt.toml", `
[[cities]]
name = "Reno, NV"
overpass = true
hex_edge_m = 80
[[include]]
path = "../a/pvmt.toml"
`)
	cfg, err := Load(top)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	reno := cityBySlug(t, cfg, "reno-nv")
	if got := cfg.ResolvedHexEdge(&reno); got != 80 {
		t.Errorf("Reno hex edge = %g; want 80 (parent's own city wins)", got)
	}
	if w := cfg.LoadWarnings(); len(w) != 0 {
		t.Errorf("a parent-declared city must not warn, got %q", w)
	}
}

// TestInclude_SupersededWarningNamesEverything: the warning is the whole
// reason the hard error could be dropped, so it must carry every fact needed
// to act on it — the city name and slug, each superseded field with the kept
// and ignored values, and both include paths.
//
// It is also the headline case for the per-field union: this is the real
// examples/all Los Angeles shape, where the metro sets decay_rate + cost_tiers
// and the national list sets growth_rate. Under the old block-atomic rule LA
// kept the metro block whole and lost growth_rate entirely; now it keeps the
// metro's decay and tiers AND gains the national growth rate, and only the two
// fields both files actually set — hex_edge_m and min_hex_area — are contested.
func TestInclude_SupersededWarningNamesEverything(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "metro/pvmt.toml", `
[grid]
hex_edge_m = 125
[display]
min_hex_area = 20
[forecast]
years = 20
decay_rate = 0.03
[[forecast.cost_tiers]]
min_pci = 0
max_pci = 100
cost_per_sqm = 200.0
label = "Flat"
[[cities]]
name = "Los Angeles, CA"
overpass = true
`)
	writeTOML(t, dir, "national/pvmt.toml", `
[grid]
hex_edge_m = 150
[display]
min_hex_area = 60
[forecast]
years = 20
growth_rate = 0.005
[[cities]]
name = "Los Angeles, CA"
overpass = true
`)
	top := writeTOML(t, dir, "all/pvmt.toml", `
[[include]]
path = "../metro/pvmt.toml"
tags = ["Metro"]
[[include]]
path = "../national/pvmt.toml"
tags = ["Top 50"]
`)
	cfg, err := Load(top)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	w := onlyWarning(t, cfg)
	want := []string{
		`"Los Angeles, CA"`, `"los-angeles-ca"`, // city identity
		"hex_edge_m (125 vs 150)",               // per-field, kept vs ignored
		"min_hex_area (20 vs 60)",               // coupled threshold, reported too
		`"../metro/pvmt.toml"`,                  // winner
		`"../national/pvmt.toml"`,               // loser
		"the first include to set a field wins", // the rule, stated once
	}
	for _, s := range want {
		if !strings.Contains(w, s) {
			t.Errorf("warning missing %q\ngot: %s", s, w)
		}
	}
	// Only genuine disagreements belong in the list, or it stops meaning "what
	// you lost": years is set identically on both sides; decay_rate and
	// cost_tiers are set by the winner alone; growth_rate by the loser alone
	// (a backfill, which loses nothing).
	for _, quiet := range []string{"forecast.years", "forecast.decay_rate", "forecast.cost_tiers", "forecast.growth_rate"} {
		if strings.Contains(w, quiet) {
			t.Errorf("warning names %s, which is not a disagreement\ngot: %s", quiet, w)
		}
	}
	// The union: winner's fields kept, loser's non-conflicting fields folded in.
	la := cityBySlug(t, cfg, "los-angeles-ca")
	fc := cfg.ResolvedForecast(&la)
	if fc.DecayRate != 0.03 || len(fc.CostTiers) != 1 {
		t.Errorf("LA decay = %g, %d tiers; want 0.03 / 1 (the metro set them and wins)",
			fc.DecayRate, len(fc.CostTiers))
	}
	if fc.GrowthRate != 0.005 {
		t.Errorf("LA growth = %g; want 0.005 backfilled from the national list, which is the only file that sets it", fc.GrowthRate)
	}
	if got := cfg.ResolvedHexEdge(&la); got != 125 {
		t.Errorf("LA hex edge = %g; want 125", got)
	}
	if got := cfg.ResolvedMinHexArea(&la); got != 20 {
		t.Errorf("LA min hex area = %g; want 20", got)
	}
	if !equalStrings(la.Tags, []string{"Metro", "Top 50"}) {
		t.Errorf("LA tags = %v; want [Metro Top 50] — the losing include still contributes tags", la.Tags)
	}
}

// TestInclude_BackfillsUnsetFieldsSilently is the headline of the per-field
// union and the fix for the citation loss it replaced: a field the winning
// include leaves UNSET is filled from a later include instead of being
// discarded, and that is not a disagreement, so it must not warn.
//
// Before the union, the national list's cited initial_pci and current_budget
// were dropped for five named cities in examples/all, replacing a cited PCI of
// 63 with the default 85 and disabling the solvency metrics that gate on
// CurrentBudget > 0 — publishing a fabricated optimistic 20-year projection
// for a named city.
func TestInclude_BackfillsUnsetFieldsSilently(t *testing.T) {
	dir := t.TempDir()
	// The metro tunes the grid and decay locally but cites no PCI or budget.
	writeTOML(t, dir, "metro/pvmt.toml", `
[grid]
hex_edge_m = 125
[display]
min_hex_area = 20
[forecast]
decay_rate = 0.03
[[cities]]
name = "San Diego, CA"
overpass = true
`)
	// The national list carries the cited figures and nothing that collides.
	writeTOML(t, dir, "national/pvmt.toml", `
[forecast]
growth_rate = 0.005
years = 30
treatment_cycle_years = 8
[[forecast.cost_tiers]]
min_pci = 0
max_pci = 100
cost_per_sqm = 90.0
label = "Flat"
[[cities]]
name = "San Diego, CA"
overpass = true
forecast.initial_pci = 63
forecast.current_budget = 83100000
`)
	top := writeTOML(t, dir, "all/pvmt.toml", `
[[include]]
path = "../metro/pvmt.toml"
[[include]]
path = "../national/pvmt.toml"
`)
	cfg, err := Load(top)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if w := cfg.LoadWarnings(); len(w) != 0 {
		t.Errorf("a pure backfill loses nothing and must not warn, got %q", w)
	}
	sd := cityBySlug(t, cfg, "san-diego-ca")
	fc := cfg.ResolvedForecast(&sd)
	checks := []struct {
		field     string
		got, want float64
	}{
		{"decay_rate (metro's, kept)", fc.DecayRate, 0.03},
		{"initial_pci (cited, backfilled)", fc.InitialPCI, 63},
		{"current_budget (cited, backfilled)", fc.CurrentBudget, 83100000},
		{"growth_rate (backfilled)", fc.GrowthRate, 0.005},
		{"treatment_cycle_years (backfilled)", fc.TreatmentCycleYears, 8},
		{"hex_edge_m (metro's, kept)", cfg.ResolvedHexEdge(&sd), 125},
		{"min_hex_area (metro's, kept)", cfg.ResolvedMinHexArea(&sd), 20},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("San Diego %s = %g; want %g", c.field, c.got, c.want)
		}
	}
	if fc.Years != 30 {
		t.Errorf("San Diego years = %d; want 30 (backfilled)", fc.Years)
	}
	if len(fc.CostTiers) != 1 {
		t.Errorf("San Diego cost_tiers = %d; want 1 (backfilled)", len(fc.CostTiers))
	}
}

// TestInclude_NoFieldIsDroppedSilently closes the hole the old code had in the
// opposite direction from the one it guarded: hex_edge_m and min_hex_area were
// checked only when the FIRST include had set them, so a value contributed by
// a later include when the first left it unset vanished with no warning at all
// — while the same situation on a forecast scalar was reported. Under the
// union both are backfills. Asserted for every calibration field so the two
// families stay consistent.
func TestInclude_NoFieldIsDroppedSilently(t *testing.T) {
	dir := t.TempDir()
	// First include: a bare city, no calibration whatsoever.
	writeTOML(t, dir, "first/pvmt.toml", "[[cities]]\nname = \"Reno, NV\"\noverpass = true\n")
	// Second include: sets every calibration field there is.
	writeTOML(t, dir, "second/pvmt.toml", `
[grid]
hex_edge_m = 55
[display]
min_hex_area = 60
[forecast]
initial_pci = 63
decay_rate = 0.05
growth_rate = 0.005
years = 30
treatment_cycle_years = 8
current_budget = 1234567
[[forecast.cost_tiers]]
min_pci = 0
max_pci = 100
cost_per_sqm = 90.0
label = "Flat"
[[cities]]
name = "Reno, NV"
overpass = true
`)
	top := writeTOML(t, dir, "all/pvmt.toml", `
[[include]]
path = "../first/pvmt.toml"
[[include]]
path = "../second/pvmt.toml"
`)
	cfg, err := Load(top)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if w := cfg.LoadWarnings(); len(w) != 0 {
		t.Errorf("nothing was dropped, so nothing should warn, got %q", w)
	}
	reno := cityBySlug(t, cfg, "reno-nv")
	fc := cfg.ResolvedForecast(&reno)
	if got := cfg.ResolvedHexEdge(&reno); got != 55 {
		t.Errorf("hex_edge_m = %g; want 55 — silently dropped before the union", got)
	}
	if got := cfg.ResolvedMinHexArea(&reno); got != 60 {
		t.Errorf("min_hex_area = %g; want 60 — silently dropped before the union", got)
	}
	if fc.InitialPCI != 63 || fc.DecayRate != 0.05 || fc.GrowthRate != 0.005 ||
		fc.Years != 30 || fc.TreatmentCycleYears != 8 || fc.CurrentBudget != 1234567 ||
		len(fc.CostTiers) != 1 {
		t.Errorf("forecast did not fully backfill: %+v", fc)
	}
}

// TestInclude_WinnerIsDeclarationOrderNotFileContent: swap the two includes and
// the other file wins. Nothing about a file's contents (more fields set, finer
// grid, larger numbers) may decide the winner — only where the [[include]] sits
// in the parent, which is the one thing the user controls by editing.
func TestInclude_WinnerIsDeclarationOrderNotFileContent(t *testing.T) {
	metro := `
[grid]
hex_edge_m = 125
[forecast]
decay_rate = 0.03
[[cities]]
name = "Los Angeles, CA"
overpass = true
`
	national := `
[grid]
hex_edge_m = 150
[forecast]
decay_rate = 0.06
[[cities]]
name = "Los Angeles, CA"
overpass = true
`
	load := func(t *testing.T, firstPath, secondPath string) *Config {
		t.Helper()
		dir := t.TempDir()
		writeTOML(t, dir, "metro/pvmt.toml", metro)
		writeTOML(t, dir, "national/pvmt.toml", national)
		top := writeTOML(t, dir, "all/pvmt.toml",
			"[[include]]\npath = \""+firstPath+"\"\n[[include]]\npath = \""+secondPath+"\"\n")
		cfg, err := Load(top)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		return cfg
	}

	t.Run("metro first", func(t *testing.T) {
		cfg := load(t, "../metro/pvmt.toml", "../national/pvmt.toml")
		la := cityBySlug(t, cfg, "los-angeles-ca")
		if got := cfg.ResolvedHexEdge(&la); got != 125 {
			t.Errorf("hex edge = %g; want 125", got)
		}
		if got := cfg.ResolvedForecast(&la).DecayRate; got != 0.03 {
			t.Errorf("decay = %g; want 0.03", got)
		}
		w := onlyWarning(t, cfg)
		if !strings.Contains(w, "hex_edge_m (125 vs 150)") ||
			!strings.Contains(w, `keeping include "../metro/pvmt.toml"`) {
			t.Errorf("warning should name the metro as the winner\ngot: %s", w)
		}
	})

	t.Run("national first", func(t *testing.T) {
		cfg := load(t, "../national/pvmt.toml", "../metro/pvmt.toml")
		la := cityBySlug(t, cfg, "los-angeles-ca")
		if got := cfg.ResolvedHexEdge(&la); got != 150 {
			t.Errorf("hex edge = %g; want 150 (declaration order, not the finer grid)", got)
		}
		if got := cfg.ResolvedForecast(&la).DecayRate; got != 0.06 {
			t.Errorf("decay = %g; want 0.06", got)
		}
		w := onlyWarning(t, cfg)
		if !strings.Contains(w, "hex_edge_m (150 vs 125)") ||
			!strings.Contains(w, `keeping include "../national/pvmt.toml"`) {
			t.Errorf("warning should name the national list as the winner\ngot: %s", w)
		}
	})
}

// TestInclude_NestedAndDiamondOrdering: nesting is the one shape where "first"
// is not obvious. all -> {west -> {ca, nv}, national}: the depth-first walk
// makes ca's city the first contribution, so ca wins over national even though
// national is a sibling of west rather than of ca. The diamond (national also
// included directly by all, and by west) must not double-count: the memoized
// child resolves once, so the same disagreement is recorded once.
//
// It also pins the warning's include paths: they are rendered relative to the
// top-level config, so the nested file reads as "../ca/pvmt.toml" from all/
// rather than by whatever spelling west/ used.
func TestInclude_NestedAndDiamondOrdering(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "ca/pvmt.toml", `
[grid]
hex_edge_m = 125
[[cities]]
name = "Los Angeles, CA"
overpass = true
`)
	writeTOML(t, dir, "nv/pvmt.toml", "[[cities]]\nname = \"Reno, NV\"\noverpass = true\n")
	writeTOML(t, dir, "national/pvmt.toml", `
[grid]
hex_edge_m = 150
[[cities]]
name = "Los Angeles, CA"
overpass = true
`)
	writeTOML(t, dir, "west/pvmt.toml", `
[[include]]
path = "../ca/pvmt.toml"
tags = ["CA"]
[[include]]
path = "../nv/pvmt.toml"
tags = ["NV"]
[[include]]
path = "../national/pvmt.toml"
tags = ["Top 50"]
`)
	top := writeTOML(t, dir, "all/pvmt.toml", `
[[include]]
path = "../west/pvmt.toml"
tags = ["West"]
[[include]]
path = "../national/pvmt.toml"
tags = ["Top 50"]
`)
	cfg, err := Load(top)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	la := cityBySlug(t, cfg, "los-angeles-ca")
	if got := cfg.ResolvedHexEdge(&la); got != 125 {
		t.Errorf("LA hex edge = %g; want 125 — ca is reached first by the depth-first walk", got)
	}
	// One line from inside west (ca vs national), and one from the outer file
	// (west's merged LA vs national again). Both are real: they are different
	// [[include]] sites, and each is the user's to reconcile.
	w := cfg.LoadWarnings()
	if len(w) != 2 {
		t.Fatalf("want 2 warnings (one per include site that lost), got %d: %q", len(w), w)
	}
	for _, line := range w {
		if !strings.Contains(line, "hex_edge_m (125 vs 150)") {
			t.Errorf("warning should report the contested edge\ngot: %s", line)
		}
		if strings.Contains(line, dir) {
			t.Errorf("include paths must be relative to the top-level config, not absolute\ngot: %s", line)
		}
	}
	// Rendered from all/, so the nested file is two hops up-and-over.
	if !strings.Contains(w[0], `"../ca/pvmt.toml"`) {
		t.Errorf("nested winner should be named relative to the top-level config\ngot: %s", w[0])
	}
	if !equalStrings(la.Tags, []string{"West", "CA", "Top 50"}) {
		t.Errorf("LA tags = %v; want [West CA Top 50] — the outer include site tags first, then the nested ones", la.Tags)
	}
}

// TestInclude_ThreeIncludesWarnPerLosingSite: a city in three includes yields
// one warning per losing include, each naming the include that actually lost —
// not two copies of the same pair — and the first include still wins every
// field it set while the others backfill what it did not.
func TestInclude_ThreeIncludesWarnPerLosingSite(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "a/pvmt.toml", `
[grid]
hex_edge_m = 80
[forecast]
decay_rate = 0.03
[[cities]]
name = "Reno, NV"
overpass = true
`)
	writeTOML(t, dir, "b/pvmt.toml", `
[grid]
hex_edge_m = 55
[forecast]
growth_rate = 0.005
[[cities]]
name = "Reno, NV"
overpass = true
`)
	writeTOML(t, dir, "c/pvmt.toml", `
[grid]
hex_edge_m = 200
[forecast]
years = 30
[[cities]]
name = "Reno, NV"
overpass = true
`)
	top := writeTOML(t, dir, "all/pvmt.toml", `
[[include]]
path = "../a/pvmt.toml"
[[include]]
path = "../b/pvmt.toml"
[[include]]
path = "../c/pvmt.toml"
`)
	cfg, err := Load(top)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	w := cfg.LoadWarnings()
	if len(w) != 2 {
		t.Fatalf("want 2 warnings (b and c each lose the edge to a), got %d: %q", len(w), w)
	}
	if !strings.Contains(w[0], `ignoring include "../b/pvmt.toml"`) ||
		!strings.Contains(w[0], "hex_edge_m (80 vs 55)") {
		t.Errorf("first warning should name b as the loser\ngot: %s", w[0])
	}
	if !strings.Contains(w[1], `ignoring include "../c/pvmt.toml"`) ||
		!strings.Contains(w[1], "hex_edge_m (80 vs 200)") {
		t.Errorf("second warning should name c as the loser\ngot: %s", w[1])
	}
	for _, line := range w {
		if !strings.Contains(line, `keeping include "../a/pvmt.toml"`) {
			t.Errorf("a set the edge first and must win both times\ngot: %s", line)
		}
	}
	// Every non-conflicting field still lands, from whichever include set it.
	reno := cityBySlug(t, cfg, "reno-nv")
	fc := cfg.ResolvedForecast(&reno)
	if fc.DecayRate != 0.03 || fc.GrowthRate != 0.005 || fc.Years != 30 {
		t.Errorf("Reno forecast = decay %g growth %g years %d; want 0.03 / 0.005 / 30 (one field from each include)",
			fc.DecayRate, fc.GrowthRate, fc.Years)
	}
}

// TestInclude_CostTiersDifferingSetsSameLength: two different tier sets of the
// same length must not print "(4 vs 4 tiers)", which reads as a formatting bug
// rather than a finding. Also covers the comparison the guard uses: an empty
// tier list and an omitted one are the same thing, and must not manufacture a
// warning for a city whose calibration is semantically identical.
func TestInclude_CostTiersDifferingSetsSameLength(t *testing.T) {
	tiers := func(cost float64) string {
		return `
[[forecast.cost_tiers]]
min_pci = 0
max_pci = 50
cost_per_sqm = ` + plainFloat(cost) + `
label = "Low"
[[forecast.cost_tiers]]
min_pci = 50
max_pci = 100
cost_per_sqm = 10.0
label = "High"
[[cities]]
name = "Reno, NV"
overpass = true
`
	}

	t.Run("same length different sets", func(t *testing.T) {
		dir := t.TempDir()
		writeTOML(t, dir, "a/pvmt.toml", "[forecast]"+tiers(200))
		writeTOML(t, dir, "b/pvmt.toml", "[forecast]"+tiers(120))
		top := writeTOML(t, dir, "all/pvmt.toml",
			"[[include]]\npath = \"../a/pvmt.toml\"\n[[include]]\npath = \"../b/pvmt.toml\"\n")
		cfg, err := Load(top)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		w := onlyWarning(t, cfg)
		if !strings.Contains(w, "different tier sets") {
			t.Errorf("warning should say the sets differ, not compare counts\ngot: %s", w)
		}
		if strings.Contains(w, "(2 vs 2 tiers)") {
			t.Errorf("warning prints an identical-looking count pair\ngot: %s", w)
		}
		reno := cityBySlug(t, cfg, "reno-nv")
		if got := cfg.ResolvedForecast(&reno).CostTiers[0].CostPerSqM; got != 200 {
			t.Errorf("kept tier cost = %g; want 200 (first include wins)", got)
		}
	})

	t.Run("empty list equals omitted", func(t *testing.T) {
		dir := t.TempDir()
		writeTOML(t, dir, "a/pvmt.toml", "[forecast]\ndecay_rate = 0.03\n[[cities]]\nname = \"Reno, NV\"\noverpass = true\n")
		writeTOML(t, dir, "b/pvmt.toml", "[forecast]\ndecay_rate = 0.03\ncost_tiers = []\n[[cities]]\nname = \"Reno, NV\"\noverpass = true\n")
		top := writeTOML(t, dir, "all/pvmt.toml",
			"[[include]]\npath = \"../a/pvmt.toml\"\n[[include]]\npath = \"../b/pvmt.toml\"\n")
		cfg, err := Load(top)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if w := cfg.LoadWarnings(); len(w) != 0 {
			t.Errorf("an empty cost_tiers list is not a disagreement with an omitted one, got %q", w)
		}
	})
}

// TestInclude_SupersededOnlyWarnsForDisagreeingCities is the examples/all
// shape: several metro files plus one broad national list, most cities unique
// to one include. Exactly the overlapping-and-disagreeing cities warn, one
// line each, and the whole thing loads — which is the bug this fixes
// (examples/all could not be loaded at all).
func TestInclude_SupersededOnlyWarnsForDisagreeingCities(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "bay/pvmt.toml", `
[forecast]
decay_rate = 0.04
[[cities]]
name = "San Jose, CA"
overpass = true
[[cities]]
name = "Oakland, CA"
overpass = true
`)
	writeTOML(t, dir, "boston/pvmt.toml", `
[forecast]
decay_rate = 0.065
[[cities]]
name = "Boston, MA"
overpass = true
`)
	// Overlaps both metros. Disagrees with bay on San Jose's decay rate;
	// agrees with boston on Boston's; Austin is unique to this file.
	writeTOML(t, dir, "top/pvmt.toml", `
[forecast]
decay_rate = 0.065
[[cities]]
name = "San Jose, CA"
overpass = true
[[cities]]
name = "Boston, MA"
overpass = true
[[cities]]
name = "Austin, TX"
overpass = true
`)
	top := writeTOML(t, dir, "all/pvmt.toml", `
[[include]]
path = "../bay/pvmt.toml"
tags = ["Bay Area"]
[[include]]
path = "../boston/pvmt.toml"
tags = ["Greater Boston"]
[[include]]
path = "../top/pvmt.toml"
tags = ["Top 50"]
`)
	cfg, err := Load(top)
	if err != nil {
		t.Fatalf("an examples/all-shaped union must load, got %v", err)
	}
	if len(cfg.Cities) != 4 {
		t.Fatalf("want 4 unique cities, got %d", len(cfg.Cities))
	}
	w := onlyWarning(t, cfg)
	if !strings.Contains(w, "San Jose") {
		t.Errorf("the one warning should be San Jose's, got %q", w)
	}
	// Boston overlaps but agrees; Oakland and Austin appear once.
	for _, quiet := range []string{"Boston", "Oakland", "Austin"} {
		if strings.Contains(w, quiet) {
			t.Errorf("%s must not warn (agrees or appears once), got %q", quiet, w)
		}
	}
	sj := cityBySlug(t, cfg, "san-jose-ca")
	if got := cfg.ResolvedForecast(&sj).DecayRate; got != 0.04 {
		t.Errorf("San Jose decay = %g; want 0.04 (bay came first)", got)
	}
	if !equalStrings(sj.Tags, []string{"Bay Area", "Top 50"}) {
		t.Errorf("San Jose tags = %v; want [Bay Area Top 50]", sj.Tags)
	}
}

// TestLoadWarnings_DoNotAffectHash: Config.Hash() is the snapshot key, and for
// in-memory configs it falls back to a TOML re-encode of the struct — so a new
// Config field can silently invalidate every stored snapshot. loadWarnings is
// unexported (the encoder skips it) precisely so it cannot. Guard both hash
// paths: the file-loaded one (raw bytes) and the in-memory fallback.
func TestLoadWarnings_DoNotAffectHash(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "a/pvmt.toml", "[grid]\nhex_edge_m = 80\n[[cities]]\nname = \"Reno, NV\"\noverpass = true\n")
	writeTOML(t, dir, "b/pvmt.toml", "[grid]\nhex_edge_m = 55\n[[cities]]\nname = \"Reno, NV\"\noverpass = true\n")
	top := writeTOML(t, dir, "all/pvmt.toml", "[[include]]\npath = \"../a/pvmt.toml\"\n[[include]]\npath = \"../b/pvmt.toml\"\n")
	cfg, err := Load(top)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.LoadWarnings()) == 0 {
		t.Fatal("expected a superseded-calibration warning to exercise the field")
	}
	// Clear the memoized content hash first. Hash() short-circuits on it for
	// any file-loaded config, so asserting stability with it in place would
	// only prove the memo works — not the claim, which is about the reflection
	// re-encode. This drives a realistic loaded config down the fallback path.
	cfg.contentHash = ""
	withWarnings := cfg.Hash()
	cfg.loadWarnings = nil
	if got := cfg.Hash(); got != withWarnings {
		t.Errorf("loaded config's fallback Hash() changed with warnings: %s vs %s", withWarnings, got)
	}

	// In-memory path (no contentHash): the reflection re-encode must ignore
	// the field too.
	mem := &Config{Cities: []CityConfig{{Name: "Reno, NV", Overpass: true}}}
	base := mem.Hash()
	mem.loadWarnings = []string{"a warning that must not move the hash"}
	if got := mem.Hash(); got != base {
		t.Errorf("in-memory Hash() changed with warnings: %s vs %s", base, got)
	}
}

// TestInclude_SlugCollisionDifferentNames errors rather than dropping a city.
func TestInclude_SlugCollisionDifferentNames(t *testing.T) {
	dir := t.TempDir()
	// Both names slugify to "st-louis" but are different cities.
	writeTOML(t, dir, "a/pvmt.toml", "[[cities]]\nname = \"St. Louis\"\noverpass = true\n")
	writeTOML(t, dir, "b/pvmt.toml", "[[cities]]\nname = \"St Louis\"\noverpass = true\n")
	top := writeTOML(t, dir, "all/pvmt.toml", `
[[include]]
path = "../a/pvmt.toml"
[[include]]
path = "../b/pvmt.toml"
`)
	_, err := Load(top)
	if err == nil {
		t.Fatal("expected slug-collision error, got nil")
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("expected ErrInvalidConfig, got %v", err)
	}
}

// TestInclude_ThinFileLoads: an include-only file (no [[cities]]) loads once
// the merge supplies cities, and fails when it supplies none.
func TestInclude_ThinFileLoads(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "leaf/pvmt.toml", "[[cities]]\nname = \"Reno, NV\"\noverpass = true\n")
	top := writeTOML(t, dir, "all/pvmt.toml", "[[include]]\npath = \"../leaf/pvmt.toml\"\ntags = [\"West\"]\n")
	cfg, err := Load(top)
	if err != nil {
		t.Fatalf("thin include-only file should load: %v", err)
	}
	if len(cfg.Cities) != 1 {
		t.Fatalf("expected 1 city, got %d", len(cfg.Cities))
	}

	// An include pointing at a config that yields no cities post-merge fails.
	empty := writeTOML(t, dir, "empty/pvmt.toml", "[[include]]\npath = \"./nonexistent-dir/pvmt.toml\"\n")
	if _, err := Load(empty); err == nil {
		t.Fatal("expected error for unreadable include")
	}
}

// TestInclude_CycleDetected: A includes B includes A.
func TestInclude_CycleDetected(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "a/pvmt.toml", "[[include]]\npath = \"../b/pvmt.toml\"\n")
	writeTOML(t, dir, "b/pvmt.toml", "[[include]]\npath = \"../a/pvmt.toml\"\n")
	if _, err := Load(filepath.Join(dir, "a/pvmt.toml")); err == nil {
		t.Fatal("expected include-cycle error, got nil")
	}
}

// TestInclude_HashFoldsIncludedFiles: editing an included file changes the
// merged config's Hash(), so snapshots invalidate and recompute triggers.
func TestInclude_HashFoldsIncludedFiles(t *testing.T) {
	dir := t.TempDir()
	leaf := writeTOML(t, dir, "leaf/pvmt.toml", "[forecast]\ndecay_rate = 0.04\n[[cities]]\nname = \"Reno, NV\"\noverpass = true\n")
	top := writeTOML(t, dir, "all/pvmt.toml", "[[include]]\npath = \"../leaf/pvmt.toml\"\ntags = [\"West\"]\n")

	cfg1, err := Load(top)
	if err != nil {
		t.Fatal(err)
	}
	h1 := cfg1.Hash()

	// Edit the included file; the top-level file is unchanged.
	if err := os.WriteFile(leaf, []byte("[forecast]\ndecay_rate = 0.09\n[[cities]]\nname = \"Reno, NV\"\noverpass = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load(top)
	if err != nil {
		t.Fatal(err)
	}
	if cfg1.Hash() != h1 {
		t.Fatal("Hash() must be stable for a given config value")
	}
	if cfg2.Hash() == h1 {
		t.Error("editing an included file must change the merged config hash")
	}
}

// TestInclude_NoIncludeHashUnchanged: a plain single-file config hashes as the
// single-blob application of the length-prefixed hashBlobs scheme (vtk3). This
// pins that a no-include config still hashes deterministically off its raw
// bytes; the value differs from the pre-vtk3 raw-bytes hash (pre-1.0, snapshots
// simply recompute).
func TestInclude_NoIncludeHashUnchanged(t *testing.T) {
	dir := t.TempDir()
	data := "[[cities]]\nname = \"Reno, NV\"\noverpass = true\n"
	p := writeTOML(t, dir, "solo/pvmt.toml", data)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.Hash(), hashBlobs([][]byte{[]byte(data)}); got != want {
		t.Errorf("no-include hash = %q; want %q (single length-prefixed blob)", got, want)
	}
	// The delimiter must actually participate: the length-prefixed hash of a
	// single blob differs from the bare-bytes hash it used before vtk3.
	if cfg.Hash() == hashBytes([]byte(data)) {
		t.Error("length-prefixed hash must differ from the pre-vtk3 bare-bytes hash")
	}
}

// TestInclude_LoadFSRejects: includes require a real path.
func TestInclude_LoadFSRejects(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "leaf/pvmt.toml", "[[cities]]\nname = \"Reno, NV\"\n")
	writeTOML(t, dir, "all/pvmt.toml", "[[include]]\npath = \"../leaf/pvmt.toml\"\n")
	if _, err := LoadFS(os.DirFS(dir), "all/pvmt.toml"); err == nil {
		t.Fatal("LoadFS must reject include-bearing configs")
	}
}

// TestInclude_RejectsTopLevelForecast: an including file must keep its top-level
// [grid]/[forecast] empty, else the flatten would silently re-calibrate included
// cities (solvent-streets-agwc).
func TestInclude_RejectsTopLevelForecast(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "leaf/pvmt.toml", "[[cities]]\nname = \"Reno, NV\"\n")

	forecastTop := writeTOML(t, dir, "fc/pvmt.toml",
		"[forecast]\ngrowth_rate = 0.01\n[[include]]\npath = \"../leaf/pvmt.toml\"\n")
	if _, err := Load(forecastTop); err == nil || !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("top-level [forecast] under [[include]] should be ErrInvalidConfig, got %v", err)
	}

	gridTop := writeTOML(t, dir, "grid/pvmt.toml",
		"[grid]\nhex_edge_m = 80\n[[include]]\npath = \"../leaf/pvmt.toml\"\n")
	if _, err := Load(gridTop); err == nil || !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("top-level [grid] under [[include]] should be ErrInvalidConfig, got %v", err)
	}
}

// TestInclude_TransitiveNesting: an included file that itself declares
// [[include]]. Grandchild tags accumulate through both include sites and the
// grandchild's calibration flattens onto its cities (solvent-streets-8je6).
func TestInclude_TransitiveNesting(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "grandchild/pvmt.toml", `
[forecast]
decay_rate = 0.06
[[cities]]
name = "Reno, NV"
overpass = true
`)
	writeTOML(t, dir, "child/pvmt.toml", `
[[include]]
path = "../grandchild/pvmt.toml"
tags = ["Nested"]
[[cities]]
name = "Sparks, NV"
overpass = true
`)
	top := writeTOML(t, dir, "all/pvmt.toml", `
[[include]]
path = "../child/pvmt.toml"
tags = ["West"]
`)

	cfg, err := Load(top)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Cities) != 2 {
		t.Fatalf("expected 2 cities (Reno, Sparks), got %d: %+v", len(cfg.Cities), cfg.Cities)
	}
	// Reno flows up two include levels: grandchild -> child ([Nested]) -> top
	// ([West]). Each merge prepends the include-site tag, so the outer "West"
	// lands before the inner "Nested".
	reno := cityBySlug(t, cfg, "reno-nv")
	if !equalStrings(reno.Tags, []string{"West", "Nested"}) {
		t.Errorf("Reno tags = %v; want [West Nested]", reno.Tags)
	}
	// The grandchild's decay calibration survives the two-level flatten.
	rfc := cfg.ResolvedForecast(&reno)
	if rfc.DecayRate != 0.06 {
		t.Errorf("Reno decay = %g; want 0.06 (grandchild calibration)", rfc.DecayRate)
	}
	// Sparks came from the child directly and picks up only the top include's tag.
	sparks := cityBySlug(t, cfg, "sparks-nv")
	if !equalStrings(sparks.Tags, []string{"West"}) {
		t.Errorf("Sparks tags = %v; want [West]", sparks.Tags)
	}
}

// TestInclude_DiamondParsedOnce: the same leaf reached via two parents is
// parsed exactly once within a single Load (scpj memoization) yet still merges
// once with the union of both paths' tags. Before memoization the leaf was
// re-read and re-parsed on the second path.
func TestInclude_DiamondParsedOnce(t *testing.T) {
	dir := t.TempDir()
	leaf := writeTOML(t, dir, "leaf/pvmt.toml", "[[cities]]\nname = \"Reno, NV\"\noverpass = true\n")
	writeTOML(t, dir, "left/pvmt.toml", "[[include]]\npath = \"../leaf/pvmt.toml\"\ntags = [\"Left\"]\n")
	writeTOML(t, dir, "right/pvmt.toml", "[[include]]\npath = \"../leaf/pvmt.toml\"\ntags = [\"Right\"]\n")
	top := writeTOML(t, dir, "all/pvmt.toml", `
[[include]]
path = "../left/pvmt.toml"
[[include]]
path = "../right/pvmt.toml"
`)

	// Count how many times each file is actually parsed during this Load.
	parses := map[string]int{}
	prev := loadParseHook
	loadParseHook = func(abs string) { parses[abs]++ }
	t.Cleanup(func() { loadParseHook = prev })

	cfg, err := Load(top)
	if err != nil {
		t.Fatalf("diamond include should load, got %v", err)
	}

	// The shared leaf must be parsed exactly once despite two include paths.
	leafAbs, err := filepath.Abs(leaf)
	if err != nil {
		t.Fatal(err)
	}
	if got := parses[leafAbs]; got != 1 {
		t.Errorf("leaf parsed %d times; want 1 (memoized across the diamond)", got)
	}
	// Every file in the diamond parses exactly once: top, left, right, leaf.
	if got := len(parses); got != 4 {
		t.Errorf("parsed %d distinct files; want 4 (top,left,right,leaf): %v", got, parses)
	}

	if len(cfg.Cities) != 1 {
		t.Fatalf("expected Reno merged once, got %d cities: %+v", len(cfg.Cities), cfg.Cities)
	}
	reno := cityBySlug(t, cfg, "reno-nv")
	if !equalStrings(reno.Tags, []string{"Left", "Right"}) {
		t.Errorf("Reno tags = %v; want [Left Right]", reno.Tags)
	}
}

// TestInclude_ThreeNodeCycle: A -> B -> C -> A is a cycle, caught cleanly rather
// than recursing forever (solvent-streets-8je6).
func TestInclude_ThreeNodeCycle(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "a/pvmt.toml", "[[include]]\npath = \"../b/pvmt.toml\"\n")
	writeTOML(t, dir, "b/pvmt.toml", "[[include]]\npath = \"../c/pvmt.toml\"\n")
	writeTOML(t, dir, "c/pvmt.toml", "[[include]]\npath = \"../a/pvmt.toml\"\n")
	_, err := Load(filepath.Join(dir, "a/pvmt.toml"))
	if err == nil || !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("3-node cycle should be ErrInvalidConfig, got %v", err)
	}
}

// TestInclude_AbsolutePath: an include path given absolutely (not ../relative)
// resolves (solvent-streets-8je6).
func TestInclude_AbsolutePath(t *testing.T) {
	dir := t.TempDir()
	leaf := writeTOML(t, dir, "leaf/pvmt.toml", "[[cities]]\nname = \"Reno, NV\"\noverpass = true\n")
	top := writeTOML(t, dir, "all/pvmt.toml", "[[include]]\npath = \""+leaf+"\"\ntags = [\"West\"]\n")
	cfg, err := Load(top)
	if err != nil {
		t.Fatalf("absolute include path should load, got %v", err)
	}
	if len(cfg.Cities) != 1 {
		t.Fatalf("expected 1 city, got %d", len(cfg.Cities))
	}
}

// TestInclude_FlattensHexEdge: an included example's [grid] hex_edge_m flattens
// onto its merged cities so it survives the empty-top-level collapse
// (solvent-streets-8je6, effectiveHexEdge).
func TestInclude_FlattensHexEdge(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "leaf/pvmt.toml", `
[grid]
hex_edge_m = 80
[[cities]]
name = "Reno, NV"
overpass = true
[[cities]]
name = "Sparks, NV"
overpass = true
hex_edge_m = 55
`)
	top := writeTOML(t, dir, "all/pvmt.toml", "[[include]]\npath = \"../leaf/pvmt.toml\"\n")
	cfg, err := Load(top)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Reno inherits the leaf's top-level grid; Sparks keeps its per-city override.
	reno := cityBySlug(t, cfg, "reno-nv")
	if got := cfg.ResolvedHexEdge(&reno); got != 80 {
		t.Errorf("Reno hex edge = %g; want 80 (flattened from leaf [grid])", got)
	}
	sparks := cityBySlug(t, cfg, "sparks-nv")
	if got := cfg.ResolvedHexEdge(&sparks); got != 55 {
		t.Errorf("Sparks hex edge = %g; want 55 (per-city override)", got)
	}
}

// TestInclude_FlattensMinHexArea pins l51o: an included example's
// [display].min_hex_area is carried onto each of its cities by the merge, the
// way hex_edge_m is. Without the flatten the child's [display] is discarded
// with the rest of its non-[[cities]] settings and every city silently falls
// back to the parent/default threshold — which is wrong precisely because the
// example also tuned hex_edge_m, and the two are coupled.
func TestInclude_FlattensMinHexArea(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "leaf/pvmt.toml", `
[grid]
hex_edge_m = 40
[display]
min_hex_area = 20
[[cities]]
name = "Reno, NV"
overpass = true
[[cities]]
name = "Sparks, NV"
overpass = true
min_hex_area = 5
`)
	top := writeTOML(t, dir, "all/pvmt.toml", "[[include]]\npath = \"../leaf/pvmt.toml\"\n")
	cfg, err := Load(top)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// The merged parent's own [display] is empty, so an unflattened threshold
	// would resolve to DefaultMinHexArea (100), not 20.
	if cfg.Display.MinHexArea != 0 {
		t.Fatalf("merged top-level display.min_hex_area = %g; want 0 (parent's own, child's discarded)", cfg.Display.MinHexArea)
	}
	reno := cityBySlug(t, cfg, "reno-nv")
	if got := cfg.ResolvedMinHexArea(&reno); got != 20 {
		t.Errorf("Reno min hex area = %g; want 20 (flattened from leaf [display])", got)
	}
	// The coupled hex edge travels with it.
	if got := cfg.ResolvedHexEdge(&reno); got != 40 {
		t.Errorf("Reno hex edge = %g; want 40", got)
	}
	sparks := cityBySlug(t, cfg, "sparks-nv")
	if got := cfg.ResolvedMinHexArea(&sparks); got != 5 {
		t.Errorf("Sparks min hex area = %g; want 5 (per-city override)", got)
	}
}

// TestInclude_ParentMinHexAreaRejected pins the invariant half of l51o: once
// min_hex_area is per-city calibration that the merge flattens, an including
// file may no longer set the top-level form. A parent threshold sized for the
// default 100 m grid would otherwise apply to every included city that did not
// tune its own — and a metro that flattened a fine hex edge (greater-boston's
// 60 m Cambridge grid, ~9.4k m² hexes) would have every hex filtered out,
// blanking the heatmap and the /play board with no error anywhere in the
// pipeline. The gate is field-scoped: the rest of [display] stays legal in a
// parent, which the second subtest pins so a future tightening to the whole
// table breaks loudly (examples/all sets [export].title and would follow).
func TestInclude_ParentMinHexAreaRejected(t *testing.T) {
	leaf := `
[grid]
hex_edge_m = 60
[[cities]]
name = "Cambridge, MA"
overpass = true
`
	t.Run("top-level min_hex_area is an error", func(t *testing.T) {
		dir := t.TempDir()
		writeTOML(t, dir, "leaf/pvmt.toml", leaf)
		top := writeTOML(t, dir, "all/pvmt.toml", `
[display]
min_hex_area = 20000
[[include]]
path = "../leaf/pvmt.toml"
`)
		_, err := Load(top)
		if err == nil || !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("top-level min_hex_area in an including file should be ErrInvalidConfig, got %v", err)
		}
		if !strings.Contains(err.Error(), "min_hex_area") {
			t.Errorf("error %v should name the rejected field", err)
		}
	})

	t.Run("other display/export settings stay legal", func(t *testing.T) {
		dir := t.TempDir()
		writeTOML(t, dir, "leaf/pvmt.toml", leaf)
		top := writeTOML(t, dir, "all/pvmt.toml", `
[display]
units = "metric"
[export]
title = "Combined"
[[include]]
path = "../leaf/pvmt.toml"
`)
		if _, err := Load(top); err != nil {
			t.Fatalf("[display].units / [export].title in an including file must stay legal, got %v", err)
		}
	})
}

// TestInclude_PerCityMinHexAreaBeatsParent exercises the resolution l51o exists
// to arbitrate, end-to-end through a real merge rather than a struct literal: a
// child that tuned min_hex_area keeps its value, while a sibling city that set
// nothing falls through to the parent's top level. The parent value has to be
// injected post-Load, since a parent that declares it in TOML is now rejected
// outright (TestInclude_ParentMinHexAreaRejected) — this is the runtime shape a
// programmatically-built config can still reach, and it pins that the flattened
// per-city value wins rather than being shadowed.
func TestInclude_PerCityMinHexAreaBeatsParent(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "leaf/pvmt.toml", `
[display]
min_hex_area = 20
[[cities]]
name = "Reno, NV"
overpass = true
`)
	top := writeTOML(t, dir, "all/pvmt.toml", `
[[include]]
path = "../leaf/pvmt.toml"
[[cities]]
name = "Elko, NV"
overpass = true
`)
	cfg, err := Load(top)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cfg.Display.MinHexArea = 500

	reno := cityBySlug(t, cfg, "reno-nv")
	if got := cfg.ResolvedMinHexArea(&reno); got != 20 {
		t.Errorf("Reno min hex area = %g; want 20 (flattened per-city value beats the parent's 500)", got)
	}
	// The parent's own city never went through the flatten, so it inherits.
	elko := cityBySlug(t, cfg, "elko-nv")
	if got := cfg.ResolvedMinHexArea(&elko); got != 500 {
		t.Errorf("Elko min hex area = %g; want 500 (no per-city value, inherits the top level)", got)
	}
}

// TestInclude_ConflictingMinHexArea: the sliver threshold gets the same
// treatment as the hex edge it is coupled to (l51o) — first-include-wins plus
// a warning naming the field. Originally (l51o) this was a hard error; vf6m
// inverted that along with hex_edge_m and forecast, since first-winning on one
// and erroring on the other would be incoherent either way round. An unset or
// identical value on the second include still defers to the first, silently.
func TestInclude_ConflictingMinHexArea(t *testing.T) {
	writeThree := func(t *testing.T, aDisplay, bDisplay string) string {
		t.Helper()
		dir := t.TempDir()
		writeTOML(t, dir, "a/pvmt.toml", aDisplay+`
[[cities]]
name = "Reno, NV"
overpass = true
`)
		writeTOML(t, dir, "b/pvmt.toml", bDisplay+`
[[cities]]
name = "Reno, NV"
overpass = true
`)
		return writeTOML(t, dir, "all/pvmt.toml", `
[[include]]
path = "../a/pvmt.toml"
[[include]]
path = "../b/pvmt.toml"
`)
	}

	t.Run("conflict keeps the first include and warns", func(t *testing.T) {
		top := writeThree(t, "[display]\nmin_hex_area = 20\n", "[display]\nmin_hex_area = 60\n")
		cfg, err := Load(top)
		if err != nil {
			t.Fatalf("conflicting min_hex_area should load with a warning, got %v", err)
		}
		reno := cityBySlug(t, cfg, "reno-nv")
		if got := cfg.ResolvedMinHexArea(&reno); got != 20 {
			t.Errorf("Reno min hex area = %g; want 20 (first include wins)", got)
		}
		if w := onlyWarning(t, cfg); !strings.Contains(w, "min_hex_area (20 vs 60)") {
			t.Errorf("warning %q should name the superseded field and both values", w)
		}
	})

	t.Run("identical is fine", func(t *testing.T) {
		top := writeThree(t, "[display]\nmin_hex_area = 20\n", "[display]\nmin_hex_area = 20\n")
		cfg, err := Load(top)
		if err != nil {
			t.Fatalf("identical min_hex_area should merge cleanly, got %v", err)
		}
		reno := cityBySlug(t, cfg, "reno-nv")
		if got := cfg.ResolvedMinHexArea(&reno); got != 20 {
			t.Errorf("Reno min hex area = %g; want 20", got)
		}
		if w := cfg.LoadWarnings(); len(w) != 0 {
			t.Errorf("identical min_hex_area must not warn, got %q", w)
		}
	})

	t.Run("unset on second defers to first", func(t *testing.T) {
		top := writeThree(t, "[display]\nmin_hex_area = 20\n", "")
		cfg, err := Load(top)
		if err != nil {
			t.Fatalf("unset second min_hex_area should merge cleanly, got %v", err)
		}
		reno := cityBySlug(t, cfg, "reno-nv")
		if got := cfg.ResolvedMinHexArea(&reno); got != 20 {
			t.Errorf("Reno min hex area = %g; want 20 (first include's value kept)", got)
		}
	})
}

// TestInclude_ParentOwnCitiesAndIncludes: a parent that declares its own
// [[cities]] and also includes. A slug present in both keeps the parent's
// direct city and unions the include's tags in (solvent-streets-8je6).
func TestInclude_ParentOwnCitiesAndIncludes(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "leaf/pvmt.toml", `
[forecast]
decay_rate = 0.09
[[cities]]
name = "Reno, NV"
overpass = true
[[cities]]
name = "Sparks, NV"
overpass = true
`)
	top := writeTOML(t, dir, "all/pvmt.toml", `
[[cities]]
name = "Reno, NV"
overpass = true
forecast.decay_rate = 0.02
[[include]]
path = "../leaf/pvmt.toml"
tags = ["West"]
`)
	cfg, err := Load(top)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Cities) != 2 {
		t.Fatalf("expected 2 cities (Reno, Sparks), got %d: %+v", len(cfg.Cities), cfg.Cities)
	}
	// Reno was declared directly first; the include only unions its tag and does
	// not overwrite the parent's own calibration.
	reno := cityBySlug(t, cfg, "reno-nv")
	if !equalStrings(reno.Tags, []string{"West"}) {
		t.Errorf("Reno tags = %v; want [West]", reno.Tags)
	}
	if rfc := cfg.ResolvedForecast(&reno); rfc.DecayRate != 0.02 {
		t.Errorf("Reno decay = %g; want 0.02 (parent's own city wins)", rfc.DecayRate)
	}
}

// TestInclude_MalformedChild: a syntax/unknown-key error in an included file is
// wrapped with that file's path so the user can locate it (solvent-streets-8je6).
func TestInclude_MalformedChild(t *testing.T) {
	dir := t.TempDir()
	leaf := writeTOML(t, dir, "leaf/pvmt.toml", "[[cities]]\nnaem = \"Reno, NV\"\n") // typo: naem
	top := writeTOML(t, dir, "all/pvmt.toml", "[[include]]\npath = \"../leaf/pvmt.toml\"\n")
	_, err := Load(top)
	if err == nil {
		t.Fatal("expected error for malformed included file")
	}
	if !strings.Contains(err.Error(), leaf) {
		t.Errorf("error should name the offending file %q; got %v", leaf, err)
	}
}

// TestInclude_DepthCap: a directory symlink pointing at its own ancestor makes
// every include level a distinct path, defeating the string-keyed cycle check;
// the depth cap stops it cleanly instead of exhausting the stack
// (solvent-streets-ndit).
func TestInclude_DepthCap(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "x")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// pvmt.toml includes ./sub/pvmt.toml, and sub is a symlink back to ".", so
	// the include path grows without bound: x/sub/pvmt.toml, x/sub/sub/..., each
	// a distinct cleaned absolute path.
	if err := os.WriteFile(filepath.Join(root, "pvmt.toml"),
		[]byte("[[include]]\npath = \"./sub/pvmt.toml\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".", filepath.Join(root, "sub")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	_, err := Load(filepath.Join(root, "pvmt.toml"))
	if err == nil || !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("symlink pseudo-cycle should hit the depth cap as ErrInvalidConfig, got %v", err)
	}
}

// TestInclude_NonRegularFile: an include pointing at a directory (a stand-in for
// any non-regular file, e.g. a FIFO that would otherwise block os.ReadFile) is
// rejected cleanly (solvent-streets-ndit).
func TestInclude_NonRegularFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "leaf"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Point the include at the directory itself, not a file inside it.
	top := writeTOML(t, dir, "all/pvmt.toml", "[[include]]\npath = \"../leaf\"\n")
	_, err := Load(top)
	if err == nil || !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("include of a non-regular file should be ErrInvalidConfig, got %v", err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestInclude_SourceIdentityMatchesStandaloneLoad is the whole point of the
// source-identity stamp, expressed the only way that can't drift: load a config
// as an INCLUDE, load the same file STANDALONE, and require that the stamped
// identity equals what the standalone load produces. If those ever disagree,
// the union config reads a different cities row or a different snapshot than
// the example's own `pvmt compute` wrote.
//
// This is a pure-config test — no DB, no export — and it is specifically the
// test that catches stamping child.Hash() instead of hashBlobs(childBlobs).
// That bug does not fail loudly: an included file's bare-bytes contentHash is a
// real hash that real (older) snapshots were written under, so the union would
// silently export stale data.
func TestInclude_SourceIdentityMatchesStandaloneLoad(t *testing.T) {
	dir := t.TempDir()
	leafA := writeTOML(t, dir, "leaf-a/pvmt.toml",
		"config_id = \"leaf-a\"\n\n[grid]\nhex_edge_m = 80\n\n[[cities]]\nname = \"Reno, NV\"\noverpass = true\n")
	// Deliberately no config_id: exercises the absolute-path fallback that lives
	// only in Load and would otherwise leave SourceConfigID empty.
	leafB := writeTOML(t, dir, "leaf-b/pvmt.toml",
		"[grid]\nhex_edge_m = 120\n\n[[cities]]\nname = \"Boise, ID\"\noverpass = true\n")
	top := writeTOML(t, dir, "all/pvmt.toml",
		"[[include]]\npath = \"../leaf-a/pvmt.toml\"\n\n[[include]]\npath = \"../leaf-b/pvmt.toml\"\n")

	union, err := Load(top)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		path, slug string
		wantEdge   float64
	}{
		{leafA, "reno-nv", 80},
		{leafB, "boise-id", 120},
	} {
		standalone, sErr := Load(tc.path)
		if sErr != nil {
			t.Fatal(sErr)
		}
		city := cityBySlug(t, union, tc.slug)

		if got, want := city.SourceConfigHash, standalone.Hash(); got != want {
			t.Errorf("%s: SourceConfigHash = %q; want %q (the hash its own compute writes)", tc.slug, got, want)
		}
		if got, want := city.SourceConfigID, standalone.ConfigID; got != want {
			t.Errorf("%s: SourceConfigID = %q; want %q", tc.slug, got, want)
		}
		if city.SourceConfigID == "" {
			t.Errorf("%s: SourceConfigID is empty; the union would fall back to its own namespace", tc.slug)
		}
		if got := city.SourceHexEdgeM; got != tc.wantEdge {
			t.Errorf("%s: SourceHexEdgeM = %g; want %g", tc.slug, got, tc.wantEdge)
		}
		// And the resolvers must agree with the stamp.
		if got := union.CityHash(&city); got != standalone.Hash() {
			t.Errorf("%s: CityHash() = %q; want %q", tc.slug, got, standalone.Hash())
		}
		if got := union.CityConfigID(&city); got != standalone.ConfigID {
			t.Errorf("%s: CityConfigID() = %q; want %q", tc.slug, got, standalone.ConfigID)
		}
	}
}

// TestInclude_SourceIdentityNotTheBareBytesHash makes the specific wrong answer
// impossible to reintroduce: hashBytes over the included file's own bytes is
// what parseConfig leaves on child.contentHash, and it is NOT what Load
// computes. Asserting inequality here means a future refactor that "simplifies"
// sourceIdentity to child.Hash() fails immediately rather than shipping.
func TestInclude_SourceIdentityNotTheBareBytesHash(t *testing.T) {
	dir := t.TempDir()
	leafData := "config_id = \"leaf\"\n\n[[cities]]\nname = \"Reno, NV\"\noverpass = true\n"
	writeTOML(t, dir, "leaf/pvmt.toml", leafData)
	top := writeTOML(t, dir, "all/pvmt.toml", "[[include]]\npath = \"../leaf/pvmt.toml\"\n")

	union, err := Load(top)
	if err != nil {
		t.Fatal(err)
	}
	city := cityBySlug(t, union, "reno-nv")
	if city.SourceConfigHash == hashBytes([]byte(leafData)) {
		t.Error("SourceConfigHash is the bare-bytes hash parseConfig sets; " +
			"it must be hashBlobs over the file, which is what Load and compute use")
	}
	if city.SourceConfigHash != hashBlobs([][]byte{[]byte(leafData)}) {
		t.Error("SourceConfigHash must equal hashBlobs of the included file's blobs")
	}
}

// TestInclude_NestedSourceIdentityKeepsDeclaringFile pins the preserve-if-set
// rule. A city declared in a leaf, pulled into a middle config, and then into a
// top config must still point at the LEAF — that is where its data lives.
// Unconditional stamping at each level would relabel it as the middle file's,
// silently, and only for users who nest includes.
func TestInclude_NestedSourceIdentityKeepsDeclaringFile(t *testing.T) {
	dir := t.TempDir()
	leaf := writeTOML(t, dir, "leaf/pvmt.toml",
		"config_id = \"leaf\"\n\n[[cities]]\nname = \"Reno, NV\"\noverpass = true\n")
	writeTOML(t, dir, "mid/pvmt.toml",
		"config_id = \"mid\"\n\n[[include]]\npath = \"../leaf/pvmt.toml\"\n")
	top := writeTOML(t, dir, "all/pvmt.toml",
		"config_id = \"top\"\n\n[[include]]\npath = \"../mid/pvmt.toml\"\n")

	union, err := Load(top)
	if err != nil {
		t.Fatal(err)
	}
	standaloneLeaf, err := Load(leaf)
	if err != nil {
		t.Fatal(err)
	}
	city := cityBySlug(t, union, "reno-nv")
	if got := city.SourceConfigID; got != "leaf" {
		t.Errorf("SourceConfigID = %q; want \"leaf\" — the file that DECLARED the city", got)
	}
	if got, want := city.SourceConfigHash, standaloneLeaf.Hash(); got != want {
		t.Errorf("SourceConfigHash = %q; want %q (the leaf's own hash)", got, want)
	}
}

// TestInclude_DirectCityHasNoSourceIdentity: a city the config declares itself
// is not an included city, keeps an empty stamp, and resolves to the config's
// own id and hash. This is the case that preserves the deliberate
// Hash()/ConfigID divergence for unrelated files that share a slug.
func TestInclude_DirectCityHasNoSourceIdentity(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "leaf/pvmt.toml",
		"config_id = \"leaf\"\n\n[[cities]]\nname = \"Reno, NV\"\noverpass = true\n")
	top := writeTOML(t, dir, "all/pvmt.toml",
		"config_id = \"top\"\n\n[[include]]\npath = \"../leaf/pvmt.toml\"\n\n"+
			"[[cities]]\nname = \"Bend, OR\"\noverpass = true\n")

	union, err := Load(top)
	if err != nil {
		t.Fatal(err)
	}
	bend := cityBySlug(t, union, "bend-or")
	if bend.SourceConfigID != "" || bend.SourceConfigHash != "" {
		t.Errorf("a directly-declared city must carry no source stamp, got id=%q hash=%q",
			bend.SourceConfigID, bend.SourceConfigHash)
	}
	if got := union.CityConfigID(&bend); got != "top" {
		t.Errorf("CityConfigID() = %q; want the config's own %q", got, "top")
	}
	if got := union.CityHash(&bend); got != union.Hash() {
		t.Errorf("CityHash() = %q; want the config's own %q", got, union.Hash())
	}
}

// TestInclude_BackfilledGridMovesSourceIdentity pins the first half of
// solvent-streets-1h6j.
//
// mergeIncludedCities stamps SourceHexEdgeM/SourceConfigID/SourceConfigHash
// preserve-if-set, i.e. from the FIRST include to declare the city. The old
// per-field union then let a LATER include backfill hex_edge_m without moving
// any of them, so the city resolved one edge and claimed another as its source.
// export.RequireMatchingHexEdge aborts on that and serve 409s, with nothing in
// the union file the user could edit to fix it — and LoadWarnings said nothing,
// because a backfill is not a disagreement.
//
// Adopting the grid now moves the identity with it: the include that supplies
// the edge is the include whose stored hexes get read.
func TestInclude_BackfilledGridMovesSourceIdentity(t *testing.T) {
	dir := t.TempDir()
	// First include declares the city with no grid calibration of any kind.
	writeTOML(t, dir, "first/pvmt.toml", `
config_id = "first"
[[cities]]
name = "Livermore, CA"
overpass = true
`)
	// Second include declares the same city WITH a grid.
	writeTOML(t, dir, "second/pvmt.toml", `
config_id = "second"
[grid]
hex_edge_m = 60
[[cities]]
name = "Livermore, CA"
overpass = true
`)
	top := writeTOML(t, dir, "all/pvmt.toml", `
[[include]]
path = "../first/pvmt.toml"
[[include]]
path = "../second/pvmt.toml"
`)
	cfg, err := Load(top)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if w := cfg.LoadWarnings(); len(w) != 0 {
		t.Errorf("a backfill is not a disagreement; want no warnings, got %q", w)
	}
	liv := cityBySlug(t, cfg, "livermore-ca")
	if got := cfg.ResolvedHexEdge(&liv); got != 60 {
		t.Errorf("ResolvedHexEdge = %g, want 60 (backfilled from the second include)", got)
	}
	// The invariant RequireMatchingHexEdge checks: the resolved edge and the
	// edge the source config computed at have to be the same number.
	if liv.SourceHexEdgeM != 60 {
		t.Errorf("SourceHexEdgeM = %g, want 60; the config that supplied the "+
			"edge must also be the one credited with the data", liv.SourceHexEdgeM)
	}
	if liv.SourceConfigID != "second" {
		t.Errorf("SourceConfigID = %q, want %q; hex ids come from the grid and "+
			"hex_stats join by id, so identity has to follow the edge",
			liv.SourceConfigID, "second")
	}
}

// TestInclude_GridPairCannotSplitAcrossIncludes pins the second half of
// solvent-streets-1h6j, which is the worse of the two: it fails SILENTLY.
//
// hex_edge_m and min_hex_area used to union independently, so a city could take
// a fine grid from one include and a sliver threshold sized for a coarse one
// from another. A 60 m flat-top hex is ~9353 sq m, so a 20000 sq m threshold
// makes filterHexSlivers drop every hex in the city. RequireMatchingHexEdge
// compares only the edge and passes (60 == 60), so the failure mode is a
// SUCCESSFUL export of an empty map — no error, no warning, no clue.
func TestInclude_GridPairCannotSplitAcrossIncludes(t *testing.T) {
	dir := t.TempDir()
	// First include: a fine grid, no threshold.
	writeTOML(t, dir, "fine/pvmt.toml", `
config_id = "fine"
[[cities]]
name = "Livermore, CA"
overpass = true
hex_edge_m = 60
`)
	// Second include: a coarse grid with the threshold sized for IT.
	writeTOML(t, dir, "coarse/pvmt.toml", `
config_id = "coarse"
[grid]
hex_edge_m = 300
[display]
min_hex_area = 20000
[[cities]]
name = "Livermore, CA"
overpass = true
`)
	top := writeTOML(t, dir, "all/pvmt.toml", `
[[include]]
path = "../fine/pvmt.toml"
[[include]]
path = "../coarse/pvmt.toml"
`)
	cfg, err := Load(top)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	liv := cityBySlug(t, cfg, "livermore-ca")
	if got := cfg.ResolvedHexEdge(&liv); got != 60 {
		t.Errorf("ResolvedHexEdge = %g, want 60 (the first include wins)", got)
	}
	if got := cfg.ResolvedMinHexArea(&liv); got == 20000 {
		t.Errorf("ResolvedMinHexArea = %g: the coarse include's threshold was "+
			"paired with the fine include's 60 m edge, which drops every hex", got)
	}
	// Both discards have to be named. Silence here is the actual bug: a
	// warning is the only thing that tells the user their union is incoherent.
	w := onlyWarning(t, cfg)
	if !strings.Contains(w, "hex_edge_m (60 vs 300)") {
		t.Errorf("warning %q should name the superseded hex_edge_m", w)
	}
	if !strings.Contains(w, "min_hex_area (unset vs 20000)") {
		t.Errorf("warning %q should name the dropped min_hex_area, and spell a "+
			"kept zero as \"unset\" rather than 0", w)
	}
}
