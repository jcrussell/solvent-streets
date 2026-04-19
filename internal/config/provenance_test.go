package config

import (
	"os"
	"testing"

	"pvmt/internal/units"
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
