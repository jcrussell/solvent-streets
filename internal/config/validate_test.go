package config

import (
	"errors"
	"testing"
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
