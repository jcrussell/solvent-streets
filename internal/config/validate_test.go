package config

import (
	"errors"
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
			Cities: []CityConfig{{Name: "Oakland"}},
		},
		"per-city negative": {
			Cities: []CityConfig{{Name: "Oakland", HexEdgeM: -1}},
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

// TestConfig_MinHexAreaSqM_FallsBackToDefault pins the resolved-value
// contract: an unset (zero) or negative DisplayConfig.MinHexAreaSqM uses
// DefaultMinHexAreaSqM at read time, while a positive override wins. The
// validator rejects strictly-negative values up front, so the runtime
// only sees zero (= default) or positive overrides.
func TestConfig_MinHexAreaSqM_FallsBackToDefault(t *testing.T) {
	cases := map[string]struct {
		set  float64
		want float64
	}{
		"unset":         {0, DefaultMinHexAreaSqM},
		"override 500":  {500, 500},
		"override 50":   {50, 50},
		"override tiny": {0.01, 0.01},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &Config{Display: DisplayConfig{MinHexAreaSqM: tc.set}}
			if got := c.MinHexAreaSqM(); got != tc.want {
				t.Errorf("MinHexAreaSqM() = %v; want %v", got, tc.want)
			}
		})
	}
}

// TestConfig_CoordinateDecimals_FallsBackToDefault pins the resolved-value
// contract for the hex GeoJSON precision knob: an unset (zero) or negative
// Export.CoordinateDecimals uses DefaultCoordinateDecimals at read time,
// while a positive override wins. Mirrors MinHexAreaSqM's accessor shape so
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

func TestConfig_Validate_BoundaryRelationID_RejectsNegative(t *testing.T) {
	cfg := Config{
		Cities: []CityConfig{{Name: "Oakland", BoundaryRelationID: -1}},
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
			Cities: []CityConfig{{Name: "Oakland", BoundaryRelationID: id}},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate() rejected id=%d: %v", id, err)
		}
	}
}

func TestConfig_Validate_MinHexAreaSqM_RejectsNegative(t *testing.T) {
	cfg := Config{
		Display: DisplayConfig{MinHexAreaSqM: -1},
		Cities:  []CityConfig{{Name: "Oakland"}},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for negative min_hex_area_sqm, got nil")
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("error %v does not chain to ErrInvalidConfig", err)
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
