package config

import (
	"errors"
	"os"
	"path/filepath"
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
func TestInclude_MergesAndUnionsTags(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "bay/pvmt.toml", `
[forecast]
growth_rate = 0.005
[[cities]]
name = "San Jose, CA"
[[cities]]
name = "Oakland, CA"
`)
	writeTOML(t, dir, "top/pvmt.toml", `
[forecast]
growth_rate = 0.01
[[cities]]
name = "San Jose, CA"
[[cities]]
name = "Austin, TX"
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
// each merged city still resolves to its source example's calibration, and the
// first include wins for a city shared across includes.
func TestInclude_PreservesPerMetroCalibration(t *testing.T) {
	dir := t.TempDir()
	writeTOML(t, dir, "bay/pvmt.toml", `
[forecast]
growth_rate = 0.005
decay_rate = 0.04
[[cities]]
name = "San Jose, CA"
[[cities]]
name = "Oakland, CA"
forecast.decay_rate = 0.07
`)
	writeTOML(t, dir, "boston/pvmt.toml", `
[forecast]
growth_rate = 0.015
decay_rate = 0.065
[[cities]]
name = "San Jose, CA"
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
	// San Jose is in both bay (first) and boston: first include (bay) wins.
	if fc.GrowthRate != 0.005 || fc.DecayRate != 0.04 {
		t.Errorf("San Jose resolved forecast = growth %g decay %g; want growth 0.005 decay 0.04 (first include wins)", fc.GrowthRate, fc.DecayRate)
	}
	// Oakland's per-city decay override survives the flatten.
	oak := cityBySlug(t, cfg, "oakland-ca")
	ofc := cfg.ResolvedForecast(&oak)
	if ofc.DecayRate != 0.07 || ofc.GrowthRate != 0.005 {
		t.Errorf("Oakland resolved forecast = growth %g decay %g; want growth 0.005 decay 0.07", ofc.GrowthRate, ofc.DecayRate)
	}
}

// TestInclude_SlugCollisionDifferentNames errors rather than dropping a city.
func TestInclude_SlugCollisionDifferentNames(t *testing.T) {
	dir := t.TempDir()
	// Both names slugify to "st-louis" but are different cities.
	writeTOML(t, dir, "a/pvmt.toml", "[[cities]]\nname = \"St. Louis\"\n")
	writeTOML(t, dir, "b/pvmt.toml", "[[cities]]\nname = \"St Louis\"\n")
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
	writeTOML(t, dir, "leaf/pvmt.toml", "[[cities]]\nname = \"Reno, NV\"\n")
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
	leaf := writeTOML(t, dir, "leaf/pvmt.toml", "[forecast]\ndecay_rate = 0.04\n[[cities]]\nname = \"Reno, NV\"\n")
	top := writeTOML(t, dir, "all/pvmt.toml", "[[include]]\npath = \"../leaf/pvmt.toml\"\ntags = [\"West\"]\n")

	cfg1, err := Load(top)
	if err != nil {
		t.Fatal(err)
	}
	h1 := cfg1.Hash()

	// Edit the included file; the top-level file is unchanged.
	if err := os.WriteFile(leaf, []byte("[forecast]\ndecay_rate = 0.09\n[[cities]]\nname = \"Reno, NV\"\n"), 0o644); err != nil {
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

// TestInclude_NoIncludeHashUnchanged: a plain single-file config keeps the same
// content hash it had before includes existed (bytes.Join of one blob is that
// blob), so existing snapshots stay valid.
func TestInclude_NoIncludeHashUnchanged(t *testing.T) {
	dir := t.TempDir()
	data := "[[cities]]\nname = \"Reno, NV\"\n"
	p := writeTOML(t, dir, "solo/pvmt.toml", data)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.Hash(), hashBytes([]byte(data)); got != want {
		t.Errorf("no-include hash = %q; want %q (raw-bytes hash)", got, want)
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
