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

// TestHash_SameContentDiffPath pins the intentional divergence documented on
// Config.Hash (radv): two byte-identical pvmt.toml files at different paths
// share the same content Hash (so a compute run and a later read of the same
// content agree on the snapshot) but get different ConfigID (so two configs
// that happen to define the same city slug still land in distinct cities rows).
func TestHash_SameContentDiffPath(t *testing.T) {
	dir := t.TempDir()
	data := "[[cities]]\nname = \"Reno, NV\"\noverpass = true\n"
	a := writeTOML(t, dir, "a/pvmt.toml", data)
	b := writeTOML(t, dir, "b/pvmt.toml", data)

	ca, err := Load(a)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := Load(b)
	if err != nil {
		t.Fatal(err)
	}

	if ca.Hash() != cb.Hash() {
		t.Errorf("byte-identical configs at different paths must share Hash: %q vs %q", ca.Hash(), cb.Hash())
	}
	if ca.ConfigID == cb.ConfigID {
		t.Errorf("configs at different paths must get distinct ConfigID, both = %q", ca.ConfigID)
	}
}
