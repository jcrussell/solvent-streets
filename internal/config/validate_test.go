package config

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"testing/fstest"
)

func TestForecastConfig_Validate_RejectsBad(t *testing.T) {
	cases := map[string]ForecastConfig{
		"initial too high":    {InitialPCI: 200},
		"initial negative":    {InitialPCI: -5},
		"decay negative":      {DecayRate: -0.1},
		"decay too high":      {DecayRate: 1.5},
		"growth too high":     {GrowthRate: 1.5},
		"growth too negative": {GrowthRate: -1.0},
		"years negative":      {Years: -1},
		"tier negative cost":  {CostTiers: []CostTierCfg{{MinPCI: 0, MaxPCI: 40, CostPerSqM: -5, Label: "x"}}},
		"tier zero cost":      {CostTiers: []CostTierCfg{{MinPCI: 0, MaxPCI: 40, CostPerSqM: 0, Label: "x"}}},
		"tier inverted band":  {CostTiers: []CostTierCfg{{MinPCI: 70, MaxPCI: 40, CostPerSqM: 5, Label: "x"}}},
		"tier min negative":   {CostTiers: []CostTierCfg{{MinPCI: -1, MaxPCI: 40, CostPerSqM: 5, Label: "x"}}},
		"tier max over 101":   {CostTiers: []CostTierCfg{{MinPCI: 0, MaxPCI: 150, CostPerSqM: 5, Label: "x"}}},
		"tier empty label":    {CostTiers: []CostTierCfg{{MinPCI: 0, MaxPCI: 40, CostPerSqM: 5, Label: ""}}},
		// NaN is the case a bare range check cannot catch: every ordered
		// comparison against it is false, so `x < 0 || x > 100` admits it and
		// the value reaches `pvmt forecast`, which prints "Initial PCI: NaN"
		// and every dollar column as NaN with exit code 0. cost_overhead was
		// already covered by its `!(x > 0 && x <= 5)` form; these five were not.
		"initial NaN": {InitialPCI: math.NaN()},
		"decay NaN":   {DecayRate: math.NaN()},
		"growth NaN":  {GrowthRate: math.NaN()},
		"budget NaN":  {CurrentBudget: math.NaN()},
		"cycle NaN":   {TreatmentCycleYears: math.NaN()},
		// +Inf slips past every "must be non-negative" check for the same
		// reason a negative literal does not.
		"budget +Inf":  {CurrentBudget: math.Inf(1)},
		"initial +Inf": {InitialPCI: math.Inf(1)},
		"cycle +Inf":   {TreatmentCycleYears: math.Inf(1)},
	}
	for name, fc := range cases {
		t.Run(name, func(t *testing.T) {
			if err := fc.Validate(); err == nil {
				t.Errorf("expected error for %+v, got nil", fc)
			}
		})
	}
}

func TestForecastConfig_Validate_AcceptsOK(t *testing.T) {
	ok := ForecastConfig{InitialPCI: 85, DecayRate: 0.05, GrowthRate: 0.02, Years: 20}
	if err := ok.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	// A valid four-tier custom schedule (the los-angeles-ca example shape).
	customTiers := ForecastConfig{
		InitialPCI: 80,
		CostTiers: []CostTierCfg{
			{MinPCI: 0, MaxPCI: 25, CostPerSqM: 150, Label: "Failed"},
			{MinPCI: 25, MaxPCI: 50, CostPerSqM: 90, Label: "Poor"},
			{MinPCI: 50, MaxPCI: 70, CostPerSqM: 50, Label: "Fair"},
			{MinPCI: 70, MaxPCI: 101, CostPerSqM: 5, Label: "Good"},
		},
	}
	if err := customTiers.Validate(); err != nil {
		t.Errorf("valid custom cost_tiers rejected: %v", err)
	}
	zero := ForecastConfig{}
	if err := zero.Validate(); err != nil {
		t.Errorf("zero values must be allowed (used as 'default' sentinels): %v", err)
	}
}

// TestConfig_Validate_HexEdgeNonNegative locks in byob-input-validation.2:
// a negative hex_edge_m at any layer is rejected, and the failure chains
// to ErrInvalidConfig so the cmdutil boundary can map it to FlagError.
// Zero is explicitly accepted because HexEdge() falls back to default.
func TestConfig_Validate_HexEdgeNonNegative(t *testing.T) {
	cases := map[string]Config{
		"top-level negative": {
			Grid:   GridConfig{HexEdgeM: -10},
			Cities: []CityConfig{{Name: "Oakland", Overpass: true}},
		},
		"per-city negative": {
			Cities: []CityConfig{{Name: "Oakland", Overpass: true, HexEdgeM: -1}},
		},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected error for negative hex_edge_m, got nil")
			}
			if !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("error %v does not chain to ErrInvalidConfig", err)
			}
		})
	}
}

// TestConfig_Validate_DisplayUnits locks in solvent-streets-dfm5: an unknown
// display.units is rejected up front (chaining to ErrInvalidConfig) rather than
// silently resolving to imperial via ParseSystem's default. Empty is allowed
// (means "use the default"), and the canonical/normalized spellings pass.
func TestConfig_Validate_DisplayUnits(t *testing.T) {
	city := []CityConfig{{Name: "Oakland", Overpass: true}}

	bad := map[string]string{
		"typo":       "metirc",
		"wrong word": "metres",
		"nonsense":   "furlongs",
	}
	for name, u := range bad {
		t.Run("reject_"+name, func(t *testing.T) {
			cfg := Config{Display: DisplayConfig{Units: u}, Cities: city}
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected error for display.units=%q, got nil", u)
			}
			if !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("error %v does not chain to ErrInvalidConfig", err)
			}
		})
	}

	for _, u := range []string{"", "metric", "imperial", "Metric", " imperial "} {
		t.Run("accept_"+u, func(t *testing.T) {
			cfg := Config{Display: DisplayConfig{Units: u}, Cities: city}
			if err := cfg.Validate(); err != nil {
				t.Errorf("display.units=%q should be accepted, got %v", u, err)
			}
		})
	}
}

// TestConfig_MinHexArea_FallsBackToDefault pins the resolved-value
// contract: an unset (zero) or negative DisplayConfig.MinHexArea uses
// DefaultMinHexArea at read time, while a positive override wins. The
// validator rejects strictly-negative values up front, so the runtime
// only sees zero (= default) or positive overrides.
func TestConfig_MinHexArea_FallsBackToDefault(t *testing.T) {
	cases := map[string]struct {
		set  float64
		want float64
	}{
		"unset":         {0, DefaultMinHexArea},
		"override 500":  {500, 500},
		"override 50":   {50, 50},
		"override tiny": {0.01, 0.01},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &Config{Display: DisplayConfig{MinHexArea: tc.set}}
			if got := c.MinHexArea(); got != tc.want {
				t.Errorf("MinHexArea() = %v; want %v", got, tc.want)
			}
		})
	}
}

// TestConfig_CoordinateDecimals_FallsBackToDefault pins the resolved-value
// contract for the hex GeoJSON precision knob: an unset (zero) or negative
// Export.CoordinateDecimals uses DefaultCoordinateDecimals at read time,
// while a positive override wins. Mirrors MinHexArea's accessor shape so
// every "config knob with a default" follows one pattern.
func TestConfig_CoordinateDecimals_FallsBackToDefault(t *testing.T) {
	cases := map[string]struct {
		set  int
		want int
	}{
		"unset":       {0, DefaultCoordinateDecimals},
		"override 7":  {7, 7},
		"override 5":  {5, 5},
		"override 10": {10, 10},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &Config{Export: ExportConfig{CoordinateDecimals: tc.set}}
			if got := c.CoordinateDecimals(); got != tc.want {
				t.Errorf("CoordinateDecimals() = %v; want %v", got, tc.want)
			}
		})
	}
}

// TestConfig_BoundarySimplifyM_FallsBackToDefault pins the one place this knob
// deliberately DIVERGES from CoordinateDecimals above: it keys on
// zero-vs-nonzero, not on positive. A negative tolerance is the documented
// byte-exact opt-out, so folding it into the default (as a `> 0` resolver
// would) makes the opt-out unreachable from config.
func TestConfig_BoundarySimplifyM_FallsBackToDefault(t *testing.T) {
	cases := map[string]struct {
		set  float64
		want float64
	}{
		"unset":        {0, DefaultBoundarySimplifyM},
		"override 25":  {25, 25},
		"override 0.5": {0.5, 0.5},
		"negative opts out of simplification, does NOT take the default": {-1, -1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &Config{Export: ExportConfig{BoundarySimplifyM: tc.set}}
			if got := c.BoundarySimplifyM(); got != tc.want {
				t.Errorf("BoundarySimplifyM() = %v; want %v", got, tc.want)
			}
		})
	}
}

// TestConfig_Validate_BoundarySimplifyM covers the non-obvious rejections. NaN
// is the one that matters: TOML accepts the `nan` literal, every ordered
// comparison against it is false so a range check alone lets it through, and
// simplefeatures' RDP never terminates on a NaN threshold — the export hangs
// instead of failing. Negatives stay legal; they are the opt-out.
func TestConfig_Validate_BoundarySimplifyM(t *testing.T) {
	cases := map[string]struct {
		set     float64
		wantErr bool
	}{
		"default":       {0, false},
		"10 m":          {10, false},
		"negative":      {-1, false},
		"at max":        {1000, false},
		"over max":      {1000.1, true},
		"NaN hangs RDP": {math.NaN(), true},
		"+Inf":          {math.Inf(1), true},
		"-Inf":          {math.Inf(-1), true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := Config{
				Cities: []CityConfig{{Name: "Oakland", Overpass: true}},
				Export: ExportConfig{BoundarySimplifyM: tc.set},
			}
			err := cfg.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("Validate() = nil; want an error for boundary_simplify_m %g", tc.set)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate() = %v; want nil for boundary_simplify_m %g", err, tc.set)
			}
			if tc.wantErr && !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("error %v does not wrap ErrInvalidConfig", err)
			}
		})
	}
}

// TestLoadFS_BoundarySimplifyM_RejectsNaNLiteral closes the loop through the
// actual ingress path. TOML has a `nan` float literal and BurntSushi decodes it
// without error, so this is reachable from a real pvmt.toml — and it is the one
// bad value a range check cannot catch, since every ordered comparison against
// NaN is false. Left unguarded it does not misconfigure the export, it HANGS it.
func TestLoadFS_BoundarySimplifyM_RejectsNaNLiteral(t *testing.T) {
	for _, lit := range []string{"nan", "inf", "-inf"} {
		t.Run(lit, func(t *testing.T) {
			toml := "[export]\nboundary_simplify_m = " + lit + "\n\n[[cities]]\nname = \"Oakland, CA\"\n"
			fsys := fstest.MapFS{"pvmt.toml": &fstest.MapFile{Data: []byte(toml)}}
			_, err := LoadFS(fsys, "pvmt.toml")
			if err == nil {
				t.Fatalf("boundary_simplify_m = %s loaded clean; want a validation error", lit)
			}
			if !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("error %v does not chain to ErrInvalidConfig", err)
			}
		})
	}
}

func TestConfig_Validate_BoundaryRelationID_RejectsNegative(t *testing.T) {
	cfg := Config{
		Cities: []CityConfig{{Name: "Oakland", Overpass: true, BoundaryRelationID: -1}},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for negative boundary_relation_id, got nil")
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("error %v does not chain to ErrInvalidConfig", err)
	}
}

func TestConfig_Validate_BoundaryRelationID_AcceptsZeroAndPositive(t *testing.T) {
	cases := []int64{0, 1, 171262, 4108817}
	for _, id := range cases {
		cfg := Config{
			Cities: []CityConfig{{Name: "Oakland", Overpass: true, BoundaryRelationID: id}},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate() rejected id=%d: %v", id, err)
		}
	}
}

// TestConfig_Validate_Tags_RejectsBlank pins blank tags being caught at the
// config boundary. A blank tag groups the city under the exporter's "untagged"
// bucket instead of a named optgroup — a silent mis-grouping the author never
// asked for. unionTags strips empties, but only for cities arriving through
// [[include]]; a directly declared city reaches the exporter unfiltered.
func TestConfig_Validate_Tags_RejectsBlank(t *testing.T) {
	for _, tag := range []string{"", " ", "\t"} {
		cfg := Config{
			Cities: []CityConfig{{Name: "Oakland", Overpass: true, Tags: []string{"Bay Area", tag}}},
		}
		err := cfg.Validate()
		if err == nil {
			t.Fatalf("expected error for blank tag %q, got nil", tag)
		}
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("tag %q: error %v does not chain to ErrInvalidConfig", tag, err)
		}
	}
}

func TestConfig_Validate_Tags_AcceptsLabels(t *testing.T) {
	cfg := Config{
		Cities: []CityConfig{{Name: "San Jose", Overpass: true, Tags: []string{"Bay Area", "Top 50"}}},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() rejected valid tags: %v", err)
	}
}

func TestConfig_Validate_MinHexArea_RejectsNegative(t *testing.T) {
	cfg := Config{
		Display: DisplayConfig{MinHexArea: -1},
		Cities:  []CityConfig{{Name: "Oakland", Overpass: true}},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for negative min_hex_area, got nil")
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("error %v does not chain to ErrInvalidConfig", err)
	}
}

// TestConfig_Validate_PerCityMinHexArea_RejectsNegative mirrors the top-level
// check for the per-city override (l51o): ResolvedMinHexArea guards on `> 0`,
// so a negative literal would silently fall through to the top-level value and
// discard the threshold the author wrote. 0 is allowed — it means "inherit".
func TestConfig_Validate_PerCityMinHexArea_RejectsNegative(t *testing.T) {
	cfg := Config{
		Cities: []CityConfig{{Name: "Oakland", Overpass: true, MinHexArea: -1}},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for negative cities[].min_hex_area, got nil")
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("error %v does not chain to ErrInvalidConfig", err)
	}
	if !strings.Contains(err.Error(), "min_hex_area") {
		t.Errorf("error %v should name the offending field", err)
	}

	ok := Config{
		Cities: []CityConfig{{Name: "Oakland", Overpass: true, MinHexArea: 0}},
	}
	if err := ok.Validate(); err != nil {
		t.Errorf("cities[].min_hex_area = 0 should be accepted (inherit), got %v", err)
	}
}

// TestConfig_ResolvedMinHexArea pins the per-city resolution chain (l51o):
// per-city override > top-level [display] > DefaultMinHexArea, with a nil city
// treated as "no override". Mirrors ResolvedHexEdge, to which the threshold is
// coupled.
func TestConfig_ResolvedMinHexArea(t *testing.T) {
	cases := map[string]struct {
		top  float64
		city *CityConfig
		want float64
	}{
		"nothing set":            {0, &CityConfig{Name: "A"}, DefaultMinHexArea},
		"top level only":         {400, &CityConfig{Name: "A"}, 400},
		"city wins over top":     {400, &CityConfig{Name: "A", MinHexArea: 25}, 25},
		"city wins over default": {0, &CityConfig{Name: "A", MinHexArea: 25}, 25},
		"nil city uses top":      {400, nil, 400},
		"nil city uses default":  {0, nil, DefaultMinHexArea},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &Config{Display: DisplayConfig{MinHexArea: tc.top}}
			if got := c.ResolvedMinHexArea(tc.city); got != tc.want {
				t.Errorf("ResolvedMinHexArea() = %v; want %v", got, tc.want)
			}
		})
	}
}

// TestConfig_Validate_CoordinateDecimals locks in itca: a negative or absurdly
// large export.coordinate_decimals is rejected up front rather than silently
// falling back to DefaultCoordinateDecimals (a negative literal used to load
// clean and lose the intended precision). 0 means "use the default" and passes.
func TestConfig_Validate_CoordinateDecimals(t *testing.T) {
	city := []CityConfig{{Name: "Oakland", Overpass: true}}

	for name, dec := range map[string]int{"negative": -1, "very negative": -100, "too large": 16, "absurd": 1000} {
		t.Run("reject_"+name, func(t *testing.T) {
			cfg := Config{Export: ExportConfig{CoordinateDecimals: dec}, Cities: city}
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected error for coordinate_decimals=%d, got nil", dec)
			}
			if !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("error %v does not chain to ErrInvalidConfig", err)
			}
		})
	}

	for _, dec := range []int{0, 1, 6, 7, 15} {
		t.Run("accept", func(t *testing.T) {
			cfg := Config{Export: ExportConfig{CoordinateDecimals: dec}, Cities: city}
			if err := cfg.Validate(); err != nil {
				t.Errorf("coordinate_decimals=%d should be accepted, got %v", dec, err)
			}
		})
	}
}

// TestConfig_Validate_DataSource locks in f7l7: a city with neither overpass
// nor an arcgis_url has no data source and would silently produce an empty
// dataset, so it is rejected (chaining ErrInvalidConfig). A city with either
// source — or both — is accepted.
func TestConfig_Validate_DataSource(t *testing.T) {
	t.Run("reject no source", func(t *testing.T) {
		cfg := Config{Cities: []CityConfig{{Name: "Oakland"}}}
		err := cfg.Validate()
		if err == nil {
			t.Fatal("expected error for a city with no data source, got nil")
		}
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("error %v does not chain to ErrInvalidConfig", err)
		}
	})

	t.Run("reject whitespace arcgis_url", func(t *testing.T) {
		cfg := Config{Cities: []CityConfig{{Name: "Oakland", ArcGISURL: "   "}}}
		if err := cfg.Validate(); err == nil || !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("whitespace-only arcgis_url should be rejected, got %v", err)
		}
	})

	accept := map[string]CityConfig{
		"overpass only":   {Name: "Oakland", Overpass: true},
		"arcgis only":     {Name: "Oakland", ArcGISURL: "https://example.com/arcgis"},
		"overpass+arcgis": {Name: "Oakland", Overpass: true, ArcGISURL: "https://example.com/arcgis"},
	}
	for name, city := range accept {
		t.Run("accept "+name, func(t *testing.T) {
			cfg := Config{Cities: []CityConfig{city}}
			if err := cfg.Validate(); err != nil {
				t.Errorf("city with a data source should be accepted, got %v", err)
			}
		})
	}
}

// TestParseConfig_RejectsUnknownKeys locks in byob-input-validation.2:
// a typo in a top-level table, scalar field, per-city override, or
// cost tier must fail at load time rather than silently unmarshal to
// the zero value. The error chains to ErrInvalidConfig so the cmdutil
// boundary maps it to a FlagError (exit code 2).
func TestParseConfig_RejectsUnknownKeys(t *testing.T) {
	cases := map[string]struct {
		toml    string
		wantKey string
	}{
		"typo in top-level table": {
			toml: `[forcast]
years = 10

[[cities]]
name = "Oakland, CA"
`,
			wantKey: "forcast",
		},
		"typo in forecast field": {
			toml: `[forecast]
initialpci = 85

[[cities]]
name = "Oakland, CA"
`,
			wantKey: "forecast.initialpci",
		},
		"typo in city field": {
			toml: `[[cities]]
name = "Oakland, CA"
overpas = true
`,
			wantKey: "cities.overpas",
		},
		"typo in cost tier": {
			toml: `[[forecast.cost_tiers]]
min_pci = 0
max_pci = 40
cost_per_smq = 100
label = "Reconstruct"

[[cities]]
name = "Oakland, CA"
`,
			wantKey: "forecast.cost_tiers.cost_per_smq",
		},
		// The removed [[layers]] config (solvent-streets 2a7n.15) must now be
		// caught by the unknown-key guard instead of silently no-op'ing.
		"removed layers section": {
			toml: `[[layers]]
name = "sidewalks"
type = "geojson"
path = "sidewalks.geojson"
id_prop = "OBJECTID"

[[cities]]
name = "Oakland, CA"
`,
			wantKey: "layers",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fsys := fstest.MapFS{"pvmt.toml": &fstest.MapFile{Data: []byte(tc.toml)}}
			_, err := LoadFS(fsys, "pvmt.toml")
			if err == nil {
				t.Fatal("expected error for unknown key, got nil")
			}
			if !errors.Is(err, ErrInvalidConfig) {
				t.Errorf("error %v does not chain to ErrInvalidConfig", err)
			}
			if !strings.Contains(err.Error(), tc.wantKey) {
				t.Errorf("error %q should name the offending key %q", err.Error(), tc.wantKey)
			}
		})
	}
}

func TestConfig_Validate_ErrChainsErrInvalidConfig(t *testing.T) {
	cfg := Config{} // no cities
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty cities, got nil")
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("error %v does not chain to ErrInvalidConfig", err)
	}
	if !errors.Is(err, ErrNoCities) {
		t.Errorf("error %v does not chain to ErrNoCities", err)
	}
}

// TestLoadFS_RejectsNonFiniteFloatKnobs sweeps every float knob in the package
// through the real TOML ingress. BurntSushi/toml accepts the `nan`, `inf` and
// `-inf` literals, so all of these are reachable from a hand-written pvmt.toml.
//
// Before the finite guards, this sweep passed for six of the eight knobs below:
// forecast.{decay_rate,growth_rate,current_budget,treatment_cycle_years,
// initial_pci} and display.min_hex_area all loaded clean with a NaN value, and
// grid.hex_edge_m accepted +Inf. Only forecast.cost_overhead and
// export.boundary_simplify_m (its own test above) rejected anything.
//
// grid.hex_edge_m is the worst of them: +Inf passes resolveHexEdge's `> 0`
// gate, reaches geo.HexGrid, and yields a silently empty hex layer.
func TestLoadFS_RejectsNonFiniteFloatKnobs(t *testing.T) {
	// %s is the non-finite literal under test.
	knobs := map[string]string{
		"forecast.initial_pci":           "[forecast]\ninitial_pci = %s\n",
		"forecast.decay_rate":            "[forecast]\ndecay_rate = %s\n",
		"forecast.growth_rate":           "[forecast]\ngrowth_rate = %s\n",
		"forecast.current_budget":        "[forecast]\ncurrent_budget = %s\n",
		"forecast.treatment_cycle_years": "[forecast]\ntreatment_cycle_years = %s\n",
		"forecast.cost_overhead":         "[forecast]\ncost_overhead = %s\n",
		"display.min_hex_area":           "[display]\nmin_hex_area = %s\n",
		"grid.hex_edge_m":                "[grid]\nhex_edge_m = %s\n",
	}
	const city = "\n[[cities]]\nname = \"Oakland, CA\"\noverpass = true\n"
	for knob, tmpl := range knobs {
		for _, lit := range []string{"nan", "inf", "-inf"} {
			t.Run(knob+"="+lit, func(t *testing.T) {
				toml := fmt.Sprintf(tmpl, lit) + city
				fsys := fstest.MapFS{"pvmt.toml": &fstest.MapFile{Data: []byte(toml)}}
				if _, err := LoadFS(fsys, "pvmt.toml"); err == nil {
					t.Fatalf("%s = %s loaded clean; want a validation error", knob, lit)
				}
			})
		}
	}
}

// TestLoadFS_RejectsNonFinitePerCityKnobs mirrors the sweep above for the
// per-city overrides. validateCityFields' comments already promise these track
// the top-level checks; without the finite guard they did not.
func TestLoadFS_RejectsNonFinitePerCityKnobs(t *testing.T) {
	knobs := map[string]string{
		"cities[0].hex_edge_m":           "hex_edge_m = %s\n",
		"cities[0].min_hex_area":         "min_hex_area = %s\n",
		"cities[0].forecast.initial_pci": "[cities.forecast]\ninitial_pci = %s\n",
	}
	for knob, tmpl := range knobs {
		for _, lit := range []string{"nan", "inf", "-inf"} {
			t.Run(knob+"="+lit, func(t *testing.T) {
				toml := "[[cities]]\nname = \"Oakland, CA\"\noverpass = true\n" + fmt.Sprintf(tmpl, lit)
				fsys := fstest.MapFS{"pvmt.toml": &fstest.MapFile{Data: []byte(toml)}}
				if _, err := LoadFS(fsys, "pvmt.toml"); err == nil {
					t.Fatalf("%s = %s loaded clean; want a validation error", knob, lit)
				}
			})
		}
	}
}
