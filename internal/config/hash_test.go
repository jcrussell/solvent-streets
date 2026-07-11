package config

import "testing"

// TestHash_FallbackDeterministicWithCityForecast guards the reflection
// fallback (contentHash empty) against pointer-address nondeterminism: two
// identical in-memory configs, each carrying a per-city *ForecastConfig
// override, must hash identically. A bare fmt "%v" would render the pointer
// as an address and break this.
func TestHash_FallbackDeterministicWithCityForecast(t *testing.T) {
	newCfg := func() *Config {
		return &Config{
			Grid:     GridConfig{HexEdgeM: 100},
			Forecast: ForecastConfig{Years: 20},
			Cities: []CityConfig{
				{
					Name:     "Detroit",
					Forecast: &ForecastConfig{Years: 30, TreatmentCycleYears: 8},
				},
			},
		}
	}

	a := newCfg()
	b := newCfg()

	if a.contentHash != "" || b.contentHash != "" {
		t.Fatal("test must exercise the fallback path (contentHash must be empty)")
	}

	if got, want := a.Hash(), b.Hash(); got != want {
		t.Errorf("Hash() of identical configs: got %q, want %q", got, want)
	}

	// Stable across a copy of the struct value.
	cpy := *a
	if got, want := cpy.Hash(), a.Hash(); got != want {
		t.Errorf("Hash() after copy: got %q, want %q", got, want)
	}
}
