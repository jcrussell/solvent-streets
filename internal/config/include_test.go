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
// the merge is a clean union (a *differing* calibration is a hard error — see
// TestInclude_ConflictingCalibration).
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
// includes with *different* non-empty calibration is a hard error (3vhw),
// rather than silently keeping the first and dropping the second. Identical or
// empty calibration on the second include is fine (covered elsewhere).
func TestInclude_ConflictingCalibration(t *testing.T) {
	t.Run("forecast", func(t *testing.T) {
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
		_, err := Load(top)
		if err == nil || !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("conflicting forecast across includes should be ErrInvalidConfig, got %v", err)
		}
	})

	t.Run("hex_edge", func(t *testing.T) {
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
		_, err := Load(top)
		if err == nil || !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("conflicting hex_edge_m across includes should be ErrInvalidConfig, got %v", err)
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
	})
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
