package config

import (
	"os"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/units"
)

func TestResolveUnits_Precedence(t *testing.T) {
	tests := []struct {
		name       string
		flag       string
		env        string // "" = unset
		file       string
		wantVal    units.System
		wantSource Source
	}{
		{"flag beats env+file", "metric", "imperial", "imperial", units.Metric, Source{Kind: SourceFlag, Detail: "--units"}},
		{"env beats file", "", "metric", "imperial", units.Metric, Source{Kind: SourceEnv, Detail: "PVMT_UNITS"}},
		{"file beats default", "", "", "metric", units.Metric, Source{Kind: SourceFile, Detail: "display.units"}},
		{"default when nothing set", "", "", "", units.Imperial, Source{Kind: SourceDefault}},
		{"invalid env falls through to file", "", "bogus", "metric", units.Metric, Source{Kind: SourceFile, Detail: "display.units"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.env == "" {
				os.Unsetenv("PVMT_UNITS")
				t.Cleanup(func() { os.Unsetenv("PVMT_UNITS") })
			} else {
				t.Setenv("PVMT_UNITS", tt.env)
			}
			cfg := &Config{Display: DisplayConfig{Units: tt.file}}
			gotVal, gotSrc := cfg.resolveUnits(tt.flag)
			if gotVal != tt.wantVal {
				t.Errorf("value = %v, want %v", gotVal, tt.wantVal)
			}
			if gotSrc != tt.wantSource {
				t.Errorf("source = %v, want %v", gotSrc, tt.wantSource)
			}
		})
	}
}

func TestResolveHexEdge_Precedence(t *testing.T) {
	t.Run("env wins", func(t *testing.T) {
		t.Setenv("PVMT_HEX_EDGE_M", "250")
		cfg := &Config{Grid: GridConfig{HexEdgeM: 100}}
		v, s := cfg.resolveHexEdge()
		if v != 250 {
			t.Errorf("value = %v, want 250", v)
		}
		if s != (Source{Kind: SourceEnv, Detail: "PVMT_HEX_EDGE_M"}) {
			t.Errorf("source = %v, want env:PVMT_HEX_EDGE_M", s)
		}
	})
	t.Run("file when env unset", func(t *testing.T) {
		os.Unsetenv("PVMT_HEX_EDGE_M")
		t.Cleanup(func() { os.Unsetenv("PVMT_HEX_EDGE_M") })
		cfg := &Config{Grid: GridConfig{HexEdgeM: 75}}
		v, s := cfg.resolveHexEdge()
		if v != 75 {
			t.Errorf("value = %v, want 75", v)
		}
		if s != (Source{Kind: SourceFile, Detail: "grid.hex_edge_m"}) {
			t.Errorf("source = %v, want file:grid.hex_edge_m", s)
		}
	})
	t.Run("default when nothing set", func(t *testing.T) {
		os.Unsetenv("PVMT_HEX_EDGE_M")
		t.Cleanup(func() { os.Unsetenv("PVMT_HEX_EDGE_M") })
		cfg := &Config{}
		v, s := cfg.resolveHexEdge()
		if v != DefaultHexEdgeM {
			t.Errorf("value = %v, want %v", v, DefaultHexEdgeM)
		}
		if s != (Source{Kind: SourceDefault}) {
			t.Errorf("source = %v, want default", s)
		}
	})
	t.Run("invalid env falls through", func(t *testing.T) {
		t.Setenv("PVMT_HEX_EDGE_M", "not-a-number")
		cfg := &Config{Grid: GridConfig{HexEdgeM: 75}}
		v, s := cfg.resolveHexEdge()
		if v != 75 {
			t.Errorf("value = %v, want 75", v)
		}
		if s.Kind != SourceFile {
			t.Errorf("source kind = %v, want file", s.Kind)
		}
	})
}

func TestResolveHexEdgeForCity(t *testing.T) {
	os.Unsetenv("PVMT_HEX_EDGE_M")
	t.Cleanup(func() { os.Unsetenv("PVMT_HEX_EDGE_M") })

	cfg := &Config{Grid: GridConfig{HexEdgeM: 100}}

	t.Run("city override wins over top-level", func(t *testing.T) {
		city := &CityConfig{Name: "Detroit", HexEdgeM: 75}
		v, s := cfg.resolveHexEdgeForCity(city)
		if v != 75 {
			t.Errorf("value = %v, want 75", v)
		}
		if s.Detail != "cities[detroit].hex_edge_m" {
			t.Errorf("source detail = %q, want cities[detroit].hex_edge_m", s.Detail)
		}
	})

	t.Run("env beats per-city override", func(t *testing.T) {
		t.Setenv("PVMT_HEX_EDGE_M", "300")
		city := &CityConfig{Name: "Detroit", HexEdgeM: 75}
		v, s := cfg.resolveHexEdgeForCity(city)
		if v != 300 {
			t.Errorf("value = %v, want 300 (env wins)", v)
		}
		if s != (Source{Kind: SourceEnv, Detail: "PVMT_HEX_EDGE_M"}) {
			t.Errorf("source = %v, want env source", s)
		}
	})

	t.Run("city inherits top-level when no override", func(t *testing.T) {
		city := &CityConfig{Name: "Detroit"}
		v, s := cfg.resolveHexEdgeForCity(city)
		if v != 100 {
			t.Errorf("value = %v, want 100", v)
		}
		if s != (Source{Kind: SourceFile, Detail: "grid.hex_edge_m"}) {
			t.Errorf("source = %v, want top-level file source", s)
		}
	})
}

func TestResolveForecast_Precedence(t *testing.T) {
	os.Unsetenv("PVMT_FORECAST_YEARS")
	os.Unsetenv("PVMT_FORECAST_INITIAL_PCI")
	t.Cleanup(func() {
		os.Unsetenv("PVMT_FORECAST_YEARS")
		os.Unsetenv("PVMT_FORECAST_INITIAL_PCI")
	})

	t.Run("env beats city beats file", func(t *testing.T) {
		t.Setenv("PVMT_FORECAST_YEARS", "7")
		cfg := &Config{Forecast: ForecastConfig{Years: 20, InitialPCI: 85}}
		city := &CityConfig{Name: "Test", Forecast: &ForecastConfig{Years: 15}}
		fc, prov := cfg.resolveForecast(city)
		if fc.Years != 7 {
			t.Errorf("Years = %d, want 7", fc.Years)
		}
		if prov.Years != (Source{Kind: SourceEnv, Detail: "PVMT_FORECAST_YEARS"}) {
			t.Errorf("Years source = %v, want env", prov.Years)
		}
	})

	t.Run("city overrides file when env unset", func(t *testing.T) {
		os.Unsetenv("PVMT_FORECAST_YEARS")
		cfg := &Config{Forecast: ForecastConfig{Years: 20, InitialPCI: 85}}
		city := &CityConfig{Name: "Test", Forecast: &ForecastConfig{Years: 15}}
		fc, prov := cfg.resolveForecast(city)
		if fc.Years != 15 {
			t.Errorf("Years = %d, want 15", fc.Years)
		}
		if prov.Years != (Source{Kind: SourceFile, Detail: "cities[test].forecast.years"}) {
			t.Errorf("Years source = %v, want city file source", prov.Years)
		}
	})

	t.Run("negative per-city growth_rate is not dropped", func(t *testing.T) {
		cfg := &Config{Forecast: ForecastConfig{GrowthRate: 0.015, InitialPCI: 85}}
		city := &CityConfig{Name: "Test", Forecast: &ForecastConfig{GrowthRate: -0.01}}
		fc, prov := cfg.resolveForecast(city)
		if fc.GrowthRate != -0.01 {
			t.Errorf("GrowthRate = %g, want -0.01 (per-city override applied)", fc.GrowthRate)
		}
		if prov.GrowthRate != (Source{Kind: SourceFile, Detail: "cities[test].forecast.growth_rate"}) {
			t.Errorf("GrowthRate source = %v, want city file source", prov.GrowthRate)
		}
	})

	t.Run("file used when nothing else set", func(t *testing.T) {
		cfg := &Config{Forecast: ForecastConfig{Years: 25, InitialPCI: 90}}
		fc, prov := cfg.resolveForecast(nil)
		if fc.Years != 25 {
			t.Errorf("Years = %d, want 25", fc.Years)
		}
		if prov.Years != (Source{Kind: SourceFile, Detail: "forecast.years"}) {
			t.Errorf("Years source = %v, want file source", prov.Years)
		}
	})

	t.Run("default when nothing set", func(t *testing.T) {
		cfg := &Config{}
		fc, prov := cfg.resolveForecast(nil)
		if fc.Years != DefaultForecastYears {
			t.Errorf("Years = %d, want %d", fc.Years, DefaultForecastYears)
		}
		if prov.Years != (Source{Kind: SourceDefault}) {
			t.Errorf("Years source = %v, want default", prov.Years)
		}
		if prov.InitialPCI != (Source{Kind: SourceDefault}) {
			t.Errorf("InitialPCI source = %v, want default", prov.InitialPCI)
		}
		if prov.DecayRate != (Source{Kind: SourceDefault}) {
			t.Errorf("DecayRate source = %v, want default", prov.DecayRate)
		}
	})
}

func TestResolve_EmitsExpectedFields(t *testing.T) {
	os.Unsetenv("PVMT_UNITS")
	os.Unsetenv("PVMT_HEX_EDGE_M")
	os.Unsetenv("PVMT_FORECAST_YEARS")
	os.Unsetenv("PVMT_FORECAST_INITIAL_PCI")
	t.Cleanup(func() {
		os.Unsetenv("PVMT_UNITS")
		os.Unsetenv("PVMT_HEX_EDGE_M")
		os.Unsetenv("PVMT_FORECAST_YEARS")
		os.Unsetenv("PVMT_FORECAST_INITIAL_PCI")
	})

	cfg := &Config{
		Display:  DisplayConfig{Units: "metric"},
		Grid:     GridConfig{HexEdgeM: 100},
		Forecast: ForecastConfig{Years: 25},
		Cities: []CityConfig{
			{Name: "Detroit", HexEdgeM: 75, Forecast: &ForecastConfig{Years: 30}},
			{Name: "Chicago"}, // no overrides
		},
	}

	fields := cfg.Resolve("")

	want := map[string]bool{
		"units":                          false,
		"grid.hex_edge_m":                false,
		"forecast.initial_pci":           false,
		"forecast.years":                 false,
		"cities[detroit].hex_edge_m":     false,
		"cities[detroit].forecast.years": false,
	}
	for _, f := range fields {
		if _, ok := want[f.Key]; ok {
			want[f.Key] = true
		}
	}
	for k, saw := range want {
		if !saw {
			t.Errorf("expected field %q in output", k)
		}
	}

	// Chicago has no overrides → no per-city lines for it.
	for _, f := range fields {
		if f.Key == "cities[chicago].hex_edge_m" {
			t.Errorf("Chicago has no hex_edge_m override, should not emit city line")
		}
	}
}

func TestResolve_FlagUnitsWins(t *testing.T) {
	t.Setenv("PVMT_UNITS", "metric")
	cfg := &Config{Display: DisplayConfig{Units: "imperial"}, Cities: []CityConfig{{Name: "Test"}}}
	fields := cfg.Resolve("imperial")

	for _, f := range fields {
		if f.Key == "units" {
			if f.Value != "imperial" {
				t.Errorf("units value = %v, want imperial", f.Value)
			}
			if f.Source.Kind != SourceFlag {
				t.Errorf("units source kind = %v, want flag", f.Source.Kind)
			}
			return
		}
	}
	t.Fatal("units field missing from Resolve output")
}

func TestSource_String(t *testing.T) {
	tests := []struct {
		s    Source
		want string
	}{
		{Source{Kind: SourceDefault}, "default"},
		{Source{Kind: SourceEnv, Detail: "PVMT_UNITS"}, "env:PVMT_UNITS"},
		{Source{Kind: SourceFile, Detail: "display.units"}, "file:display.units"},
		{Source{Kind: SourceFlag, Detail: "--units"}, "flag:--units"},
	}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("Source{%v}.String() = %q, want %q", tt.s, got, tt.want)
		}
	}
}

func TestResolvedField_ExportData(t *testing.T) {
	r := ResolvedField{
		Key:    "units",
		Value:  "metric",
		Source: Source{Kind: SourceEnv, Detail: "PVMT_UNITS"},
	}
	m := r.ExportData([]string{"key", "value", "source"})
	if m["key"] != "units" {
		t.Errorf("key = %v, want units", m["key"])
	}
	if m["value"] != "metric" {
		t.Errorf("value = %v, want metric", m["value"])
	}
	src, ok := m["source"].(map[string]any)
	if !ok {
		t.Fatalf("source is not a map: %v", m["source"])
	}
	if src["kind"] != "env" {
		t.Errorf("source.kind = %v, want env", src["kind"])
	}
	if src["detail"] != "PVMT_UNITS" {
		t.Errorf("source.detail = %v, want PVMT_UNITS", src["detail"])
	}

	// subset of fields
	m2 := r.ExportData([]string{"key"})
	if len(m2) != 1 || m2["key"] != "units" {
		t.Errorf("subset = %v, want only key", m2)
	}
}

// TestSourceKind_WireValues locks in the string values of the four
// SourceKind constants. These leak into `config show --json` output and
// users' jq/template scripts compare against them — renaming any of
// them silently breaks downstream tooling.
func TestSourceKind_WireValues(t *testing.T) {
	tests := []struct {
		kind SourceKind
		want string
	}{
		{SourceDefault, "default"},
		{SourceEnv, "env"},
		{SourceFile, "file"},
		{SourceFlag, "flag"},
	}
	for _, tt := range tests {
		if string(tt.kind) != tt.want {
			t.Errorf("SourceKind %v = %q, want %q (wire format is part of the public contract)", tt.kind, string(tt.kind), tt.want)
		}
	}
}

// TestResolve_AllLayersReachable is the byob-config.2 contract test: a
// single configuration must reach every layer of the precedence chain
// (flag / env / file / default) and surface the correct SourceKind for
// each. A future refactor that collapses or reorders the chain has to
// argue with this test.
func TestResolve_AllLayersReachable(t *testing.T) {
	os.Unsetenv("PVMT_UNITS")
	os.Unsetenv("PVMT_HEX_EDGE_M")
	os.Unsetenv("PVMT_FORECAST_YEARS")
	os.Unsetenv("PVMT_FORECAST_INITIAL_PCI")
	t.Cleanup(func() {
		os.Unsetenv("PVMT_UNITS")
		os.Unsetenv("PVMT_HEX_EDGE_M")
		os.Unsetenv("PVMT_FORECAST_YEARS")
		os.Unsetenv("PVMT_FORECAST_INITIAL_PCI")
	})

	// Env supplies hex_edge_m; file supplies forecast.years; no layer
	// supplies forecast.initial_pci (→ default); flag supplies units.
	t.Setenv("PVMT_HEX_EDGE_M", "250")
	cfg := &Config{
		Forecast: ForecastConfig{Years: 25},
		Cities:   []CityConfig{{Name: "Test"}},
	}
	fields := cfg.Resolve("metric")

	got := make(map[SourceKind]bool)
	byKey := make(map[string]ResolvedField)
	for _, f := range fields {
		got[f.Source.Kind] = true
		byKey[f.Key] = f
	}

	for _, want := range []SourceKind{SourceFlag, SourceEnv, SourceFile, SourceDefault} {
		if !got[want] {
			t.Errorf("no field reported source kind %q; layered chain is broken", want)
		}
	}

	if byKey["units"].Source.Kind != SourceFlag {
		t.Errorf("units source = %v, want flag", byKey["units"].Source)
	}
	if byKey["grid.hex_edge_m"].Source.Kind != SourceEnv {
		t.Errorf("grid.hex_edge_m source = %v, want env", byKey["grid.hex_edge_m"].Source)
	}
	if byKey["forecast.years"].Source.Kind != SourceFile {
		t.Errorf("forecast.years source = %v, want file", byKey["forecast.years"].Source)
	}
	if byKey["forecast.initial_pci"].Source.Kind != SourceDefault {
		t.Errorf("forecast.initial_pci source = %v, want default", byKey["forecast.initial_pci"].Source)
	}
}

// TestResolve_EveryFieldHasNonEmptySource is the property guard for
// byob-config.2: no ResolvedField may carry a zero Source. A new field
// added to Resolve() that forgets to populate Source.Kind would render
// as "(<empty>)" in `config show --sources` and "" in JSON — neither
// of which lets a user answer "where did this come from?".
func TestResolve_EveryFieldHasNonEmptySource(t *testing.T) {
	os.Unsetenv("PVMT_UNITS")
	os.Unsetenv("PVMT_HEX_EDGE_M")
	os.Unsetenv("PVMT_FORECAST_YEARS")
	os.Unsetenv("PVMT_FORECAST_INITIAL_PCI")
	t.Cleanup(func() {
		os.Unsetenv("PVMT_UNITS")
		os.Unsetenv("PVMT_HEX_EDGE_M")
		os.Unsetenv("PVMT_FORECAST_YEARS")
		os.Unsetenv("PVMT_FORECAST_INITIAL_PCI")
	})

	cfg := &Config{
		Display:  DisplayConfig{Units: "metric"},
		Grid:     GridConfig{HexEdgeM: 100},
		Forecast: ForecastConfig{Years: 25, InitialPCI: 90},
		Cities: []CityConfig{
			{Name: "Detroit", HexEdgeM: 75, Forecast: &ForecastConfig{Years: 30, InitialPCI: 95}},
			{Name: "Chicago"},
		},
	}
	for _, f := range cfg.Resolve("") {
		if f.Source.Kind == "" {
			t.Errorf("field %q has empty Source.Kind; every resolved field must declare its layer", f.Key)
		}
	}
}

// TestResolveForecast_InitialPCIEnvSource is the symmetry guard for
// the InitialPCI env layer: TestResolveForecast_Precedence already
// asserts that env beats the file layer for Years; this pins the same
// provenance for the InitialPCI knob so a future refactor that
// silently drops the env-source label only on InitialPCI fails here.
func TestResolveForecast_InitialPCIEnvSource(t *testing.T) {
	os.Unsetenv("PVMT_FORECAST_YEARS")
	t.Setenv("PVMT_FORECAST_INITIAL_PCI", "72")
	t.Cleanup(func() { os.Unsetenv("PVMT_FORECAST_INITIAL_PCI") })

	cfg := &Config{Forecast: ForecastConfig{Years: 20, InitialPCI: 85}}
	fc, prov := cfg.resolveForecast(nil)
	if fc.InitialPCI != 72 {
		t.Errorf("InitialPCI = %v, want 72 (env wins)", fc.InitialPCI)
	}
	if prov.InitialPCI != (Source{Kind: SourceEnv, Detail: "PVMT_FORECAST_INITIAL_PCI"}) {
		t.Errorf("InitialPCI source = %v, want env:PVMT_FORECAST_INITIAL_PCI", prov.InitialPCI)
	}
}
