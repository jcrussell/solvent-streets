package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// loadCity loads the config at path and returns it with the named city.
func loadCity(t *testing.T, path, slug string) (*Config, *CityConfig) {
	t.Helper()
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	for i := range cfg.Cities {
		if cfg.Cities[i].Slug() == slug {
			return cfg, &cfg.Cities[i]
		}
	}
	t.Fatalf("city %q not in %s", slug, path)
	return nil, nil
}

// cityHashOf loads TOML text and returns the named city's hash.
func cityHashOf(t *testing.T, data, slug string) string {
	t.Helper()
	cfg, city := loadCity(t, writeTOML(t, t.TempDir(), "pvmt.toml", data), slug)
	return cfg.CityHash(city)
}

const twoCities = `
[grid]
hex_edge_m = 120

[forecast]
years = 25

[[cities]]
name = "Reno, NV"
overpass = true

[[cities]]
name = "Boise, ID"
overpass = true
hex_edge_m = 90
`

// TestCityHash_Format pins the on-disk shape: 16 lowercase hex characters,
// the width of snapshots.config_hash.
func TestCityHash_Format(t *testing.T) {
	h := cityHashOf(t, twoCities, "reno-nv")
	if len(h) != 16 || strings.Trim(h, "0123456789abcdef") != "" {
		t.Errorf("CityHash = %q, want 16 lowercase hex chars", h)
	}
}

// TestCityHash_DeterministicInMemory: two identical in-memory configs, each
// carrying a per-city *ForecastConfig, hash identically (no pointer addresses
// or map order can leak into the encoding).
func TestCityHash_DeterministicInMemory(t *testing.T) {
	newCfg := func() *Config {
		return &Config{
			Grid:     GridConfig{HexEdgeM: 100},
			Forecast: ForecastConfig{Years: 20},
			Services: []ServiceOverride{{Name: "fire", TurnoutS: new(60.0)}, {Name: "ems", TurnoutS: new(30.0)}},
			Cities: []CityConfig{{
				Name:     "Detroit",
				Forecast: &ForecastConfig{Years: 30, TreatmentCycleYears: 8},
			}},
		}
	}
	a, b := newCfg(), newCfg()
	if got, want := a.CityHash(&a.Cities[0]), b.CityHash(&b.Cities[0]); got != want {
		t.Errorf("CityHash of identical configs: %q vs %q", got, want)
	}
	// [[services]] are looked up by name, so their order is not an input.
	b.Services[0], b.Services[1] = b.Services[1], b.Services[0]
	if got, want := b.CityHash(&b.Cities[0]), a.CityHash(&a.Cities[0]); got != want {
		t.Errorf("reordering [[services]] moved the hash: %q vs %q", got, want)
	}
}

// TestCityHash_FormattingEditsDoNotMove is the bug this hash exists to fix
// (solvent-streets-ern4.12): a comment, whitespace or key-order edit used to
// change the raw-bytes hash, so the next single-resource compute committed a
// partial snapshot under a new hash and export published the city without
// coverage, census or the combined total.
func TestCityHash_FormattingEditsDoNotMove(t *testing.T) {
	edited := `# A comment that was not there before.
[forecast]
  years   =   25   # trailing comment

[grid]
hex_edge_m = 120.0

[[cities]]
overpass = true
name = "Reno, NV"


[[cities]]
hex_edge_m = 90
name = "Boise, ID"
overpass = true
`
	for _, slug := range []string{"reno-nv", "boise-id"} {
		if got, want := cityHashOf(t, edited, slug), cityHashOf(t, twoCities, slug); got != want {
			t.Errorf("%s: a formatting-only edit moved the hash: %q vs %q", slug, got, want)
		}
	}
}

// TestCityHash_EditOneCityMovesOnlyThatCity: per-city keys, so recalibrating
// one city leaves every other city's snapshots readable.
func TestCityHash_EditOneCityMovesOnlyThatCity(t *testing.T) {
	edited := strings.Replace(twoCities, "hex_edge_m = 90", "hex_edge_m = 95", 1)
	if cityHashOf(t, edited, "boise-id") == cityHashOf(t, twoCities, "boise-id") {
		t.Error("editing Boise's hex_edge_m must change Boise's hash")
	}
	if got, want := cityHashOf(t, edited, "reno-nv"), cityHashOf(t, twoCities, "reno-nv"); got != want {
		t.Errorf("editing Boise moved Reno's hash: %q vs %q", got, want)
	}
	// A per-city forecast override is the same story.
	edited = strings.Replace(twoCities, "hex_edge_m = 90",
		"hex_edge_m = 90\n[cities.forecast]\ninitial_pci = 70", 1)
	if cityHashOf(t, edited, "boise-id") == cityHashOf(t, twoCities, "boise-id") {
		t.Error("a per-city forecast override must change that city's hash")
	}
	if got, want := cityHashOf(t, edited, "reno-nv"), cityHashOf(t, twoCities, "reno-nv"); got != want {
		t.Errorf("Boise's forecast override moved Reno's hash: %q vs %q", got, want)
	}
}

// TestCityHash_ExplicitDefaultEqualsOmitted: the hash is over resolved values,
// so spelling out a default config owns is the same as leaving it out.
func TestCityHash_ExplicitDefaultEqualsOmitted(t *testing.T) {
	omitted := "[[cities]]\nname = \"Reno, NV\"\noverpass = true\n"
	explicit := `
[grid]
hex_edge_m = 100

[forecast]
initial_pci = 85
years = 20
cost_overhead = 1.0
growth_rate = 0

[[cities]]
name = "Reno, NV"
overpass = true
census = true
`
	if got, want := cityHashOf(t, explicit, "reno-nv"), cityHashOf(t, omitted, "reno-nv"); got != want {
		t.Errorf("explicit defaults hash %q, omitted %q; want equal", got, want)
	}
	// The same value set at another layer resolves identically too.
	perCity := "[[cities]]\nname = \"Reno, NV\"\noverpass = true\nhex_edge_m = 100\n"
	if got, want := cityHashOf(t, perCity, "reno-nv"), cityHashOf(t, omitted, "reno-nv"); got != want {
		t.Errorf("per-city hex_edge_m = default hashes %q, omitted %q; want equal", got, want)
	}
}

// TestCityHash_DownstreamDefaultsAreHashedAsStated pins the documented
// exception: defaults that live in internal/forecast or internal/services are
// unknown to config, so stating one is a (harmless) change. If config ever
// learns those defaults, flip this test and update hash.go's doc comment.
func TestCityHash_DownstreamDefaultsAreHashedAsStated(t *testing.T) {
	omitted := "[[cities]]\nname = \"Reno, NV\"\noverpass = true\n"
	stated := "[forecast]\ntreatment_cycle_years = 12\n" + omitted // forecast.DefaultTreatmentCycleYears
	if cityHashOf(t, stated, "reno-nv") == cityHashOf(t, omitted, "reno-nv") {
		t.Error("treatment_cycle_years = 12 hashed like an omitted one; hash.go documents the opposite")
	}
}

// TestCityHash_EnvOverridesMoveTheHash (solvent-streets-ern4.15): an env
// override changes stored results, so it must change the key, or its results
// would be read back as the file's.
func TestCityHash_EnvOverridesMoveTheHash(t *testing.T) {
	path := writeTOML(t, t.TempDir(), "pvmt.toml", twoCities)
	baseCfg, baseCity := loadCity(t, path, "reno-nv")
	base := baseCfg.CityHash(baseCity)
	for _, env := range []struct{ key, val string }{
		{"PVMT_HEX_EDGE_M", "300"},
		{"PVMT_FORECAST_YEARS", "40"},
		{"PVMT_FORECAST_INITIAL_PCI", "60"},
	} {
		t.Run(env.key, func(t *testing.T) {
			t.Setenv(env.key, env.val)
			cfg, city := loadCity(t, path, "reno-nv")
			if cfg.CityHash(city) == base {
				t.Errorf("%s=%s did not change the hash", env.key, env.val)
			}
		})
	}
	// An override equal to the file's value is no change at all.
	t.Run("equal to file", func(t *testing.T) {
		t.Setenv("PVMT_FORECAST_YEARS", "25")
		cfg, city := loadCity(t, path, "reno-nv")
		if got := cfg.CityHash(city); got != base {
			t.Errorf("PVMT_FORECAST_YEARS equal to the file's years moved the hash: %q vs %q", got, base)
		}
	})
}

// TestCityHash_PresentationAndIngestEditsDoNotMove: settings that only shape
// what export renders, or what ingest fetches, leave stored results valid.
func TestCityHash_PresentationAndIngestEditsDoNotMove(t *testing.T) {
	base := "[[cities]]\nname = \"Reno, NV\"\noverpass = true\n"
	for name, data := range map[string]string{
		"display.units":        "[display]\nunits = \"metric\"\n" + base,
		"display.min_hex_area": "[display]\nmin_hex_area = 500\n" + base,
		"export":               "[export]\ntitle = \"T\"\ncoordinate_decimals = 6\nboundary_simplify_m = 25\n" + base,
		"tags":                 base + "tags = [\"West\", \"state:NV\"]\n",
		"city min_hex_area":    base + "min_hex_area = 300\n",
		"sources":              base + "arcgis_url = \"https://example.com/arcgis\"\nallow_private_arcgis = true\nboundary_relation_id = 42\n",
		"current_budget":       "[forecast]\ncurrent_budget = 5000000\n" + base,
		"travel_threshold_s":   "[[services]]\nname = \"fire\"\ntravel_threshold_s = 300\n" + base,
		"facility name":        base + "[[cities.facilities]]\nservice = \"fire\"\nlat = 39.5\nlon = -119.8\n",
	} {
		t.Run(name, func(t *testing.T) {
			ref := base
			if name == "facility name" {
				// Compare against the same facility with a different name.
				ref = data + "name = \"Station 1\"\n"
			}
			if got, want := cityHashOf(t, data, "reno-nv"), cityHashOf(t, ref, "reno-nv"); got != want {
				t.Errorf("%s edit moved the hash: %q vs %q", name, got, want)
			}
		})
	}
	// The city's name is not an input either (it keys the cities row).
	a := &Config{Cities: []CityConfig{{Name: "Reno, NV", Overpass: true}}}
	b := &Config{Cities: []CityConfig{{Name: "Sparks, NV", Overpass: true}}}
	if a.CityHash(&a.Cities[0]) != b.CityHash(&b.Cities[0]) {
		t.Error("a city's name must not be a compute input")
	}
}

// TestCityHash_ResultSettingsMove: the other side of the classification, one
// representative per group of result-affecting settings.
func TestCityHash_ResultSettingsMove(t *testing.T) {
	base := "[[cities]]\nname = \"Reno, NV\"\noverpass = true\n"
	ref := cityHashOf(t, base, "reno-nv")
	for name, data := range map[string]string{
		"census off": base + "census = false\n",
		"turnout_s":  "[[services]]\nname = \"fire\"\nturnout_s = 60\n" + base,
		// An explicit 0 is a value services.Resolve applies (travel time
		// alone), not "unset", so it must not hash like an absent turnout_s.
		"turnout_s = 0": "[[services]]\nname = \"fire\"\nturnout_s = 0\n" + base,
		"facility":      base + "[[cities.facilities]]\nservice = \"fire\"\nlat = 39.5\nlon = -119.8\n",
		"decay_rate":    "[forecast]\ndecay_rate = 0.05\n" + base,
		"growth_rate":   "[forecast]\ngrowth_rate = 0.02\n" + base,
		"cost_tiers":    "[[forecast.cost_tiers]]\nmin_pci = 0\nmax_pci = 101\ncost_per_sqm = 9\nlabel = \"flat\"\n" + base,
		"cost_overhead": "[forecast]\ncost_overhead = 1.5\n" + base,
	} {
		if cityHashOf(t, data, "reno-nv") == ref {
			t.Errorf("%s did not change the hash", name)
		}
	}
}

// TestHash_SameContentDiffPath pins the intentional divergence documented on
// Config.CityConfigID: two byte-identical pvmt.toml files at different paths
// give a city the same hash (it would compute the same results) but different
// ConfigIDs (distinct cities rows, so their snapshots never mix).
func TestHash_SameContentDiffPath(t *testing.T) {
	dir := t.TempDir()
	data := "[[cities]]\nname = \"Reno, NV\"\noverpass = true\n"
	ca, cityA := loadCity(t, writeTOML(t, dir, "a/pvmt.toml", data), "reno-nv")
	cb, cityB := loadCity(t, writeTOML(t, dir, "b/pvmt.toml", data), "reno-nv")
	if ca.CityHash(cityA) != cb.CityHash(cityB) {
		t.Errorf("identical configs at different paths must share CityHash: %q vs %q",
			ca.CityHash(cityA), cb.CityHash(cityB))
	}
	if ca.ConfigID == cb.ConfigID {
		t.Errorf("configs at different paths must get distinct ConfigID, both = %q", ca.ConfigID)
	}
}

// TestCityHash_IncludedCityMatchesSource: an [[include]]d city hashes exactly
// as its own config hashes it, whatever that config's layout — so a union
// reads the snapshots each example computed.
func TestCityHash_IncludedCityMatchesSource(t *testing.T) {
	dir := t.TempDir()
	leaf := writeTOML(t, dir, "leaf/pvmt.toml", `
config_id = "leaf"
[grid]
hex_edge_m = 80
[forecast]
years = 30
decay_rate = 0.04
[[services]]
name = "fire"
travel_threshold_s = 200
[[cities]]
name = "Reno, NV"
overpass = true
  [[cities.facilities]]
  service = "fire"
  lat = 39.5
  lon = -119.8
`)
	top := writeTOML(t, dir, "all/pvmt.toml", "[export]\ntitle = \"All\"\n[[include]]\npath = \"../leaf/pvmt.toml\"\ntags = [\"West\"]\n")

	src, srcCity := loadCity(t, leaf, "reno-nv")
	union, city := loadCity(t, top, "reno-nv")
	if got, want := union.CityHash(city), src.CityHash(srcCity); got != want {
		t.Errorf("included city hashes %q under the union, %q under its own config", got, want)
	}
	if d := union.SourceDivergence(city); len(d) != 0 {
		t.Errorf("SourceDivergence = %q, want none", d)
	}
}

// TestCityHash_IncludedForecastBackfillKeepsSourceHash: the per-field merge may
// give an included city forecast fields its source lacks (examples/all does
// this for ten cities). Every reader recomputes the forecast from its own
// config, so the union still reads the source's snapshots.
func TestCityHash_IncludedForecastBackfillKeepsSourceHash(t *testing.T) {
	dir := t.TempDir()
	metro := writeTOML(t, dir, "metro/pvmt.toml", "config_id = \"metro\"\n[grid]\nhex_edge_m = 300\n[[cities]]\nname = \"San Diego, CA\"\noverpass = true\n")
	writeTOML(t, dir, "top50/pvmt.toml", "config_id = \"top50\"\n[[cities]]\nname = \"San Diego, CA\"\noverpass = true\n[cities.forecast]\ninitial_pci = 63\n")
	top := writeTOML(t, dir, "all/pvmt.toml", "[[include]]\npath = \"../metro/pvmt.toml\"\n[[include]]\npath = \"../top50/pvmt.toml\"\n")

	src, srcCity := loadCity(t, metro, "san-diego-ca")
	union, city := loadCity(t, top, "san-diego-ca")
	if got := union.ResolvedForecast(city).InitialPCI; got != 63 {
		t.Fatalf("fixture: union initial_pci = %g, want the backfilled 63", got)
	}
	if got, want := union.CityHash(city), src.CityHash(srcCity); got != want {
		t.Errorf("a forecast backfill moved the included city off its source's hash: %q vs %q", got, want)
	}
}

// TestCityHash_IncludedDataDivergenceGetsOwnHash: when the union would compute
// different stored data than the source did, reading the source's snapshots
// would be wrong, so the city gets its own hash and SourceDivergence says why.
//
// The [[services]] case pins solvent-streets-qtue as it stands: the include
// merge does not carry an included file's [[services]], so the union resolves
// the registry defaults. Before this hash the union read the source's coverage
// (seconds with the source's turnout baked in) against default legend bands,
// silently. Now it is loud. Fixing qtue would make this case agree again —
// update the test then.
func TestCityHash_IncludedDataDivergenceGetsOwnHash(t *testing.T) {
	dir := t.TempDir()
	leaf := writeTOML(t, dir, "leaf/pvmt.toml", `
config_id = "leaf"
[[services]]
name = "fire"
turnout_s = 60
[[cities]]
name = "Reno, NV"
overpass = true
`)
	top := writeTOML(t, dir, "all/pvmt.toml", "[[include]]\npath = \"../leaf/pvmt.toml\"\n")

	src, srcCity := loadCity(t, leaf, "reno-nv")
	union, city := loadCity(t, top, "reno-nv")
	if city.SourceConfigHash != src.CityHash(srcCity) {
		t.Fatalf("stamp = %q, want the source's own hash %q", city.SourceConfigHash, src.CityHash(srcCity))
	}
	if union.CityHash(city) == src.CityHash(srcCity) {
		t.Error("the union drops the leaf's [[services]] (qtue) yet reads the leaf's coverage")
	}
	d := union.SourceDivergence(city)
	if len(d) != 1 || !strings.Contains(d[0], "[[services]]") {
		t.Errorf("SourceDivergence = %q, want one [[services]] entry", d)
	}

	// The same union, given the [[services]] at its own top level, agrees again.
	agree := writeTOML(t, dir, "agree/pvmt.toml", "[[services]]\nname = \"fire\"\nturnout_s = 60\n[[include]]\npath = \"../leaf/pvmt.toml\"\n")
	agreeCfg, agreeCity := loadCity(t, agree, "reno-nv")
	if got, want := agreeCfg.CityHash(agreeCity), src.CityHash(srcCity); got != want {
		t.Errorf("matching [[services]] in the union: hash %q, want the source's %q", got, want)
	}
}

// TestCityHash_ExamplesAllMatchesEachExample runs the include rule over the
// shipped configs: every city examples/all pulls in hashes as the example that
// declares it does, so `make site` reads the examples' snapshots. Ten of these
// cities get forecast fields their example lacks; none differs in data inputs.
func TestCityHash_ExamplesAllMatchesEachExample(t *testing.T) {
	root := filepath.Join("..", "..", "examples")
	union, err := Load(filepath.Join(root, "all", "pvmt.toml"))
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]*Config{}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "all" {
			continue
		}
		p := filepath.Join(root, e.Name(), "pvmt.toml")
		if _, err := os.Stat(p); err != nil {
			continue
		}
		cfg, err := Load(p)
		if err != nil {
			t.Fatal(err)
		}
		byID[cfg.ConfigID] = cfg
	}
	for i := range union.Cities {
		city := &union.Cities[i]
		src := byID[city.SourceConfigID]
		if src == nil {
			t.Errorf("%s: no example with config_id %q", city.Name, city.SourceConfigID)
			continue
		}
		var srcCity *CityConfig
		for j := range src.Cities {
			if src.Cities[j].Slug() == city.Slug() {
				srcCity = &src.Cities[j]
			}
		}
		if srcCity == nil {
			t.Errorf("%s: not declared by %q", city.Name, city.SourceConfigID)
			continue
		}
		if got, want := union.CityHash(city), src.CityHash(srcCity); got != want {
			t.Errorf("%s: hash %q under examples/all, %q under %s (divergence: %q)",
				city.Name, got, want, city.SourceConfigID, union.SourceDivergence(city))
		}
	}
}

// fieldClass is how a config field relates to the snapshot hash.
type fieldClass int

const (
	// nested holds config structs whose own fields are classified.
	nested fieldClass = iota + 1
	// hashed reaches computeInputs: changing it changes stored results.
	hashed
	// presentation is read only when rendering or exporting.
	presentation
	// ingest decides what gets fetched and stored; compute reads the stored
	// copy, so it does not invalidate results by itself.
	ingest
	// plumbing is identity, keys, include layout, merge stamps and presence
	// bits: not a setting in its own right.
	plumbing
)

// fieldRule classifies one field. mutate (required for hashed, presentation
// and ingest fields) changes that field on baseHashConfig, so the test can
// check the classification against what CityHash actually does.
type fieldRule struct {
	class  fieldClass
	mutate func(c *Config)
}

func baseHashConfig() *Config {
	return &Config{
		Forecast: ForecastConfig{
			CostTiers: []CostTierCfg{{MinPCI: 0, MaxPCI: 101, CostPerSqM: 10, Label: "flat"}},
		},
		Services: []ServiceOverride{{Name: "fire", TurnoutS: new(70.0), TravelThresholdS: new(240.0)}},
		Include:  []IncludeSpec{{Path: "../x/pvmt.toml", Tags: []string{"X"}}},
		Cities: []CityConfig{{
			Name: "Reno, NV", Overpass: true,
			Forecast:   &ForecastConfig{},
			Facilities: []FacilityConfig{{Service: "fire", Name: "Station 1", Lat: 39.5, Lon: -119.8}},
		}},
	}
}

// configFieldRules is the classification CONTRIBUTING.md asks a new config
// field to be added to. Keys are "Type.Field" for every struct type reachable
// from Config through nested fields.
var configFieldRules = map[string]fieldRule{
	"Config.Grid":         {class: nested},
	"Config.Display":      {class: nested},
	"Config.Export":       {class: nested},
	"Config.Forecast":     {class: nested},
	"Config.Cities":       {class: nested},
	"Config.Include":      {class: nested},
	"Config.Services":     {class: nested},
	"Config.ConfigID":     {class: plumbing},
	"Config.SourcePath":   {class: plumbing},
	"Config.loadWarnings": {class: plumbing},

	"GridConfig.HexEdgeM": {hashed, func(c *Config) { c.Grid.HexEdgeM = 250 }},

	"DisplayConfig.Units":      {presentation, func(c *Config) { c.Display.Units = "metric" }},
	"DisplayConfig.MinHexArea": {presentation, func(c *Config) { c.Display.MinHexArea = 500 }},

	"ExportConfig.Title":              {presentation, func(c *Config) { c.Export.Title = "T" }},
	"ExportConfig.CoordinateDecimals": {presentation, func(c *Config) { c.Export.CoordinateDecimals = 7 }},
	"ExportConfig.BoundarySimplifyM":  {presentation, func(c *Config) { c.Export.BoundarySimplifyM = 25 }},

	"ForecastConfig.InitialPCI":          {hashed, func(c *Config) { c.Forecast.InitialPCI = 70 }},
	"ForecastConfig.DecayRate":           {hashed, func(c *Config) { c.Forecast.DecayRate = 0.05 }},
	"ForecastConfig.GrowthRate":          {hashed, func(c *Config) { c.Forecast.GrowthRate = 0.02 }},
	"ForecastConfig.Years":               {hashed, func(c *Config) { c.Forecast.Years = 30 }},
	"ForecastConfig.CostTiers":           {class: nested},
	"ForecastConfig.TreatmentCycleYears": {hashed, func(c *Config) { c.Forecast.TreatmentCycleYears = 8 }},
	"ForecastConfig.CurrentBudget":       {presentation, func(c *Config) { c.Forecast.CurrentBudget = 1e6 }},
	"ForecastConfig.CostOverhead":        {hashed, func(c *Config) { c.Forecast.CostOverhead = 1.5 }},
	"ForecastConfig.growthRateSet":       {class: plumbing},

	"CostTierCfg.MinPCI":     {hashed, func(c *Config) { c.Forecast.CostTiers[0].MinPCI = 1 }},
	"CostTierCfg.MaxPCI":     {hashed, func(c *Config) { c.Forecast.CostTiers[0].MaxPCI = 100 }},
	"CostTierCfg.CostPerSqM": {hashed, func(c *Config) { c.Forecast.CostTiers[0].CostPerSqM = 11 }},
	"CostTierCfg.Label":      {hashed, func(c *Config) { c.Forecast.CostTiers[0].Label = "other" }},

	"CityConfig.Name":               {ingest, func(c *Config) { c.Cities[0].Name = "Sparks, NV" }},
	"CityConfig.Overpass":           {ingest, func(c *Config) { c.Cities[0].Overpass = false }},
	"CityConfig.ArcGISURL":          {ingest, func(c *Config) { c.Cities[0].ArcGISURL = "https://example.com" }},
	"CityConfig.HexEdgeM":           {hashed, func(c *Config) { c.Cities[0].HexEdgeM = 250 }},
	"CityConfig.MinHexArea":         {presentation, func(c *Config) { c.Cities[0].MinHexArea = 300 }},
	"CityConfig.Forecast":           {class: nested},
	"CityConfig.Tags":               {presentation, func(c *Config) { c.Cities[0].Tags = []string{"West"} }},
	"CityConfig.BoundaryRelationID": {ingest, func(c *Config) { c.Cities[0].BoundaryRelationID = 42 }},
	"CityConfig.Facilities":         {class: nested},
	"CityConfig.AllowPrivateArcGIS": {ingest, func(c *Config) { c.Cities[0].AllowPrivateArcGIS = true }},
	"CityConfig.Census": {hashed, func(c *Config) {
		off := false
		c.Cities[0].Census = &off
	}},
	"CityConfig.SourceConfigID":   {class: plumbing},
	"CityConfig.SourceConfigHash": {class: plumbing},
	"CityConfig.sourceData":       {class: plumbing},

	"ServiceOverride.Name":             {hashed, func(c *Config) { c.Services[0].Name = "ems" }},
	"ServiceOverride.TurnoutS":         {hashed, func(c *Config) { c.Services[0].TurnoutS = new(60.0) }},
	"ServiceOverride.TravelThresholdS": {presentation, func(c *Config) { c.Services[0].TravelThresholdS = new(300.0) }},

	"FacilityConfig.Service": {hashed, func(c *Config) { c.Cities[0].Facilities[0].Service = "ems" }},
	"FacilityConfig.Name":    {presentation, func(c *Config) { c.Cities[0].Facilities[0].Name = "Station 2" }},
	"FacilityConfig.Lat":     {hashed, func(c *Config) { c.Cities[0].Facilities[0].Lat = 39.6 }},
	"FacilityConfig.Lon":     {hashed, func(c *Config) { c.Cities[0].Facilities[0].Lon = -119.7 }},

	"IncludeSpec.Path": {class: plumbing},
	"IncludeSpec.Tags": {class: plumbing},
}

// TestComputeInputs_EveryFieldClassified is the contract that keeps the hash
// honest as Config grows: every field of every config struct must be in
// configFieldRules, and each classification is checked against CityHash. A new
// field fails here until someone decides whether it changes stored results
// (and, if so, adds it to computeInputs in hash.go). See CONTRIBUTING.md.
func TestComputeInputs_EveryFieldClassified(t *testing.T) {
	seen := map[string]bool{}
	var walk func(rt reflect.Type)
	walk = func(rt reflect.Type) {
		for f := range rt.Fields() {
			key := rt.Name() + "." + f.Name
			seen[key] = true
			rule, ok := configFieldRules[key]
			if !ok {
				t.Errorf("%s is not classified: add it to configFieldRules as hashed, presentation, "+
					"ingest or plumbing (see CONTRIBUTING.md, \"Adding a config field\")", key)
				continue
			}
			if rule.class != nested {
				continue
			}
			ft := f.Type
			for ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice {
				ft = ft.Elem()
			}
			if ft.Kind() != reflect.Struct || ft.PkgPath() != rt.PkgPath() {
				t.Errorf("%s is classified nested but %s is not a config struct", key, f.Type)
				continue
			}
			walk(ft)
		}
	}
	walk(reflect.TypeFor[Config]())
	for key := range configFieldRules {
		if !seen[key] {
			t.Errorf("configFieldRules has %s, which no longer exists", key)
		}
	}

	base := baseHashConfig()
	want := base.CityHash(&base.Cities[0])
	for key, rule := range configFieldRules {
		if rule.class == nested || rule.class == plumbing {
			continue
		}
		if rule.mutate == nil {
			t.Errorf("%s needs a mutate func to check its classification", key)
			continue
		}
		c := baseHashConfig()
		rule.mutate(c)
		moved := c.CityHash(&c.Cities[0]) != want
		switch {
		case rule.class == hashed && !moved:
			t.Errorf("%s is classified hashed but changing it leaves CityHash unchanged", key)
		case rule.class != hashed && moved:
			t.Errorf("%s is classified as not affecting results but changing it moves CityHash", key)
		}
	}
}
