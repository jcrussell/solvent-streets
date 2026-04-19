package config

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"pvmt/internal/units"
)

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Livermore, CA", "livermore-ca"},
		{"Oakland, CA", "oakland-ca"},
		{"San Francisco", "san-francisco"},
		{"simple", "simple"},
		{"  spaces  ", "spaces"},
		{"UPPER CASE", "upper-case"},
		{"a--b", "a-b"},
	}
	for _, tt := range tests {
		got := Slugify(tt.input)
		if got != tt.want {
			t.Errorf("Slugify(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCitySlug(t *testing.T) {
	c := CityConfig{Name: "Livermore, CA"}
	if got := c.Slug(); got != "livermore-ca" {
		t.Errorf("Slug() = %q, want %q", got, "livermore-ca")
	}
}

func TestLoadMultiCity(t *testing.T) {
	toml := `
[grid]
hex_edge_m = 100

[[cities]]
name = "Livermore, CA"
overpass = true

[[cities]]
name = "Oakland, CA"
overpass = true
`
	fsys := fstest.MapFS{"pvmt.toml": &fstest.MapFile{Data: []byte(toml)}}

	cfg, err := LoadFS(fsys, "pvmt.toml")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Cities) != 2 {
		t.Fatalf("expected 2 cities, got %d", len(cfg.Cities))
	}
	if cfg.Cities[0].Name != "Livermore, CA" {
		t.Errorf("expected Livermore, got %q", cfg.Cities[0].Name)
	}
	if cfg.Cities[1].Slug() != "oakland-ca" {
		t.Errorf("expected oakland-ca slug, got %q", cfg.Cities[1].Slug())
	}
	if cfg.Grid.HexEdgeM != 100 {
		t.Errorf("expected hex_edge_m 100, got %f", cfg.Grid.HexEdgeM)
	}
}

func TestValidateNoCities(t *testing.T) {
	toml := `[grid]
hex_edge_m = 100
`
	fsys := fstest.MapFS{"pvmt.toml": &fstest.MapFile{Data: []byte(toml)}}
	_, err := LoadFS(fsys, "pvmt.toml")
	if err == nil {
		t.Fatal("expected error for no cities")
	}
}

func TestValidateDuplicateCities(t *testing.T) {
	toml := `
[[cities]]
name = "Livermore, CA"

[[cities]]
name = "Livermore, CA"
`
	fsys := fstest.MapFS{"pvmt.toml": &fstest.MapFile{Data: []byte(toml)}}
	_, err := LoadFS(fsys, "pvmt.toml")
	if err == nil {
		t.Fatal("expected error for duplicate city names")
	}
}

func TestResolvedForecast_CityOverride(t *testing.T) {
	cfg := &Config{
		Forecast: ForecastConfig{
			DecayRate:  0.05,
			GrowthRate: 0.01,
			Years:      20,
			InitialPCI: 85,
		},
	}

	// No per-city override: inherits top-level.
	city := &CityConfig{Name: "Test"}
	fc := cfg.ResolvedForecast(city)
	if fc.DecayRate != 0.05 {
		t.Errorf("expected 0.05, got %f", fc.DecayRate)
	}

	// With per-city override: override wins, unoverridden fields inherit.
	cityOverride := &CityConfig{
		Name: "Test",
		Forecast: &ForecastConfig{
			DecayRate: 0.08,
			Years:     30,
		},
	}
	fc2 := cfg.ResolvedForecast(cityOverride)
	if fc2.DecayRate != 0.08 {
		t.Errorf("expected 0.08, got %f", fc2.DecayRate)
	}
	if fc2.Years != 30 {
		t.Errorf("expected 30, got %d", fc2.Years)
	}
	if fc2.GrowthRate != 0.01 {
		t.Errorf("expected inherited 0.01, got %f", fc2.GrowthRate)
	}
}

func TestResolvedForecast_InitialPCICityOverride(t *testing.T) {
	cfg := &Config{
		Forecast: ForecastConfig{
			InitialPCI: 85,
			DecayRate:  0.05,
		},
	}

	city := &CityConfig{
		Name:     "Test",
		Forecast: &ForecastConfig{InitialPCI: 77},
	}
	fc := cfg.ResolvedForecast(city)
	if fc.InitialPCI != 77 {
		t.Errorf("expected InitialPCI 77, got %f", fc.InitialPCI)
	}
	if fc.DecayRate != 0.05 {
		t.Errorf("expected inherited DecayRate 0.05, got %f", fc.DecayRate)
	}

	cityNoOverride := &CityConfig{Name: "Default"}
	fc2 := cfg.ResolvedForecast(cityNoOverride)
	if fc2.InitialPCI != 85 {
		t.Errorf("expected inherited InitialPCI 85, got %f", fc2.InitialPCI)
	}
}

// TestNormalizeForecast locks in the clamp/default semantics in one
// place so ResolvedForecast and export.ResolvedTOML can rely on them.
func TestNormalizeForecast(t *testing.T) {
	tests := []struct {
		name      string
		input     ForecastConfig
		wantPCI   float64
		wantYears int
	}{
		{"zeros become defaults", ForecastConfig{}, DefaultInitialPCI, DefaultForecastYears},
		{"explicit in-range preserved", ForecastConfig{InitialPCI: 77, Years: 30}, 77, 30},
		{"negative PCI becomes default", ForecastConfig{InitialPCI: -10, Years: 15}, DefaultInitialPCI, 15},
		{"over-100 PCI becomes default", ForecastConfig{InitialPCI: 150, Years: 15}, DefaultInitialPCI, 15},
		{"PCI boundary 100 preserved", ForecastConfig{InitialPCI: 100, Years: 5}, 100, 5},
		{"small positive PCI preserved", ForecastConfig{InitialPCI: 0.5, Years: 5}, 0.5, 5},
		{"negative Years becomes default", ForecastConfig{InitialPCI: 77, Years: -3}, 77, DefaultForecastYears},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := tt.input
			NormalizeForecast(&fc)
			if fc.InitialPCI != tt.wantPCI {
				t.Errorf("InitialPCI = %v, want %v", fc.InitialPCI, tt.wantPCI)
			}
			if fc.Years != tt.wantYears {
				t.Errorf("Years = %v, want %v", fc.Years, tt.wantYears)
			}
		})
	}
}

func TestResolvedHexEdge(t *testing.T) {
	cfg := &Config{Grid: GridConfig{HexEdgeM: 100}}
	city := &CityConfig{Name: "A"}
	if got := cfg.ResolvedHexEdge(city); got != 100 {
		t.Errorf("expected 100, got %f", got)
	}
	cityOverride := &CityConfig{Name: "B", HexEdgeM: 50}
	if got := cfg.ResolvedHexEdge(cityOverride); got != 50 {
		t.Errorf("expected 50, got %f", got)
	}
}

func TestUnitSystem_Precedence(t *testing.T) {
	tests := []struct {
		name string
		env  string // "" means unset
		file string
		want units.System
	}{
		{"env beats file", "metric", "imperial", units.Metric},
		{"file beats default", "", "metric", units.Metric},
		{"default when nothing set", "", "", units.Imperial},
		{"empty env falls through to file", "", "metric", units.Metric},
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
			if got := cfg.UnitSystem(); got != tt.want {
				t.Errorf("UnitSystem() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHexEdge_Precedence(t *testing.T) {
	t.Run("env beats file", func(t *testing.T) {
		t.Setenv("PVMT_HEX_EDGE_M", "250")
		cfg := &Config{Grid: GridConfig{HexEdgeM: 100}}
		if got := cfg.HexEdge(); got != 250 {
			t.Errorf("HexEdge() = %v, want 250", got)
		}
	})
	t.Run("file used when env unset", func(t *testing.T) {
		os.Unsetenv("PVMT_HEX_EDGE_M")
		t.Cleanup(func() { os.Unsetenv("PVMT_HEX_EDGE_M") })
		cfg := &Config{Grid: GridConfig{HexEdgeM: 75}}
		if got := cfg.HexEdge(); got != 75 {
			t.Errorf("HexEdge() = %v, want 75", got)
		}
	})
	t.Run("default when nothing set", func(t *testing.T) {
		os.Unsetenv("PVMT_HEX_EDGE_M")
		t.Cleanup(func() { os.Unsetenv("PVMT_HEX_EDGE_M") })
		cfg := &Config{}
		if got := cfg.HexEdge(); got != DefaultHexEdgeM {
			t.Errorf("HexEdge() = %v, want %v", got, DefaultHexEdgeM)
		}
	})
	t.Run("unparseable env falls through to file", func(t *testing.T) {
		t.Setenv("PVMT_HEX_EDGE_M", "not-a-number")
		cfg := &Config{Grid: GridConfig{HexEdgeM: 75}}
		if got := cfg.HexEdge(); got != 75 {
			t.Errorf("HexEdge() = %v, want 75", got)
		}
	})
	t.Run("non-positive env falls through to file", func(t *testing.T) {
		t.Setenv("PVMT_HEX_EDGE_M", "-5")
		cfg := &Config{Grid: GridConfig{HexEdgeM: 75}}
		if got := cfg.HexEdge(); got != 75 {
			t.Errorf("HexEdge() = %v, want 75", got)
		}
	})
}

// TestResolvedForecast_EnvYears is the regression guard for the layered
// resolution: PVMT_FORECAST_YEARS must actually reach callers of
// ResolvedForecast, which was not the case before the env layer was
// plumbed through the city-merge path.
func TestResolvedForecast_EnvYears(t *testing.T) {
	t.Setenv("PVMT_FORECAST_YEARS", "5")
	cfg := &Config{Forecast: ForecastConfig{Years: 20, InitialPCI: 85}}
	fc := cfg.ResolvedForecast(&CityConfig{Name: "Test"})
	if fc.Years != 5 {
		t.Errorf("expected env override to win, got %d", fc.Years)
	}
}

// TestResolvedForecast_EnvYearsNonPositiveIgnored guards the validation
// on the env-parse path: zero and negative values must not silently
// corrupt the forecast horizon.
func TestResolvedForecast_EnvYearsNonPositiveIgnored(t *testing.T) {
	t.Setenv("PVMT_FORECAST_YEARS", "0")
	cfg := &Config{Forecast: ForecastConfig{Years: 20, InitialPCI: 85}}
	fc := cfg.ResolvedForecast(&CityConfig{Name: "Test"})
	if fc.Years != 20 {
		t.Errorf("invalid env should fall through to file, got %d", fc.Years)
	}
}

// TestResolvedForecast_EnvInitialPCIClamped is the range-clamp guard:
// an out-of-range env value must be rejected at the resolve site so the
// forecast math never sees a PCI > 100 or ≤ 0.
func TestResolvedForecast_EnvInitialPCIClamped(t *testing.T) {
	t.Setenv("PVMT_FORECAST_INITIAL_PCI", "500")
	cfg := &Config{Forecast: ForecastConfig{InitialPCI: 70, Years: 20}}
	fc := cfg.ResolvedForecast(&CityConfig{Name: "Test"})
	if fc.InitialPCI != 70 {
		t.Errorf("out-of-range env should fall through to file value 70, got %f", fc.InitialPCI)
	}
}

// TestResolvedForecast_EnvBeatsCity covers the precedence ordering:
// env > city > top-level. Without this, a future refactor could silently
// swap the order and break user expectations.
func TestResolvedForecast_EnvBeatsCity(t *testing.T) {
	t.Setenv("PVMT_FORECAST_YEARS", "7")
	cfg := &Config{Forecast: ForecastConfig{Years: 20, InitialPCI: 85}}
	city := &CityConfig{Name: "Test", Forecast: &ForecastConfig{Years: 15}}
	fc := cfg.ResolvedForecast(city)
	if fc.Years != 7 {
		t.Errorf("env should beat city override, got %d", fc.Years)
	}
}

// TestResolvedForecast_DefaultsApplied confirms ResolvedForecast returns
// fully-normalized fields even when file and city leave everything zero.
// This is the invariant that lets callers read fc.InitialPCI / fc.Years
// directly without re-checking.
func TestResolvedForecast_DefaultsApplied(t *testing.T) {
	cfg := &Config{Forecast: ForecastConfig{}} // all zeros
	fc := cfg.ResolvedForecast(&CityConfig{Name: "Test"})
	if fc.InitialPCI != DefaultInitialPCI {
		t.Errorf("InitialPCI = %v, want %v", fc.InitialPCI, DefaultInitialPCI)
	}
	if fc.Years != DefaultForecastYears {
		t.Errorf("Years = %v, want %v", fc.Years, DefaultForecastYears)
	}
}

// TestLoad verifies that Load sets an absolute-style SourcePath that
// callers can later hand back to os.ReadFile, unlike LoadFS which sets
// the fs-relative name.
func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/pvmt.toml"
	data := []byte(`[[cities]]
name = "Testville"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourcePath != path {
		t.Errorf("SourcePath = %q, want %q", cfg.SourcePath, path)
	}
	if len(cfg.Cities) != 1 || cfg.Cities[0].Name != "Testville" {
		t.Errorf("unexpected cities: %+v", cfg.Cities)
	}
}

// TestLoad_MissingFileReportsFullPath locks in the B2 fix: a bad
// --config path must surface in the error so the user can see which
// path they mistyped. The old LoadFS-delegating Load leaked only the
// basename.
func TestLoad_MissingFileReportsFullPath(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/does-not-exist.toml"
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q should contain full path %q", err.Error(), path)
	}
}

// TestResolvedForecast_NilCity guards the B3 fix: ResolvedForecast must
// tolerate a nil *CityConfig and return fully-normalized defaults,
// rather than panicking on the nil deref inside applyCityForecast.
func TestResolvedForecast_NilCity(t *testing.T) {
	cfg := &Config{Forecast: ForecastConfig{Years: 15, InitialPCI: 70}}
	fc := cfg.ResolvedForecast(nil)
	if fc.Years != 15 {
		t.Errorf("Years = %d, want 15 (top-level preserved)", fc.Years)
	}
	if fc.InitialPCI != 70 {
		t.Errorf("InitialPCI = %v, want 70 (top-level preserved)", fc.InitialPCI)
	}
}

// TestResolvedForecast_NilCityAppliesDefaults is the other half of the
// nil-city contract: with a zero top-level Forecast, the normalizer
// must still fire so callers can read fc.InitialPCI / fc.Years directly.
func TestResolvedForecast_NilCityAppliesDefaults(t *testing.T) {
	cfg := &Config{Forecast: ForecastConfig{}}
	fc := cfg.ResolvedForecast(nil)
	if fc.InitialPCI != DefaultInitialPCI {
		t.Errorf("InitialPCI = %v, want %v", fc.InitialPCI, DefaultInitialPCI)
	}
	if fc.Years != DefaultForecastYears {
		t.Errorf("Years = %d, want %d", fc.Years, DefaultForecastYears)
	}
}
