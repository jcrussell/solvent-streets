package config

import (
	"os"
	"path/filepath"
	"testing"
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
	dir := t.TempDir()
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
	path := filepath.Join(dir, "pvmt.toml")
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
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
	dir := t.TempDir()
	toml := `[grid]
hex_edge_m = 100
`
	path := filepath.Join(dir, "pvmt.toml")
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for no cities")
	}
}

func TestValidateDuplicateCities(t *testing.T) {
	dir := t.TempDir()
	toml := `
[[cities]]
name = "Livermore, CA"

[[cities]]
name = "Livermore, CA"
`
	path := filepath.Join(dir, "pvmt.toml")
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for duplicate city names")
	}
}

func TestResolvedForecast(t *testing.T) {
	cfg := &Config{
		Forecast: ForecastConfig{
			DecayRate:  0.05,
			GrowthRate: 0.01,
			Years:      20,
		},
	}

	// No per-city override
	city := &CityConfig{Name: "Test"}
	fc := cfg.ResolvedForecast(city)
	if fc.DecayRate != 0.05 {
		t.Errorf("expected 0.05, got %f", fc.DecayRate)
	}

	// With per-city override
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

func TestResolvedInitialPCI(t *testing.T) {
	tests := []struct {
		input float64
		want  float64
	}{
		{0, 85.0},    // default
		{77, 77.0},   // explicit
		{-10, 85.0},  // negative → default
		{150, 85.0},  // over 100 → default
		{100, 100.0}, // boundary
		{0.5, 0.5},   // small positive
	}
	for _, tt := range tests {
		fc := &ForecastConfig{InitialPCI: tt.input}
		got := fc.ResolvedInitialPCI()
		if got != tt.want {
			t.Errorf("ResolvedInitialPCI(%v) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestResolvedForecast_InitialPCI(t *testing.T) {
	cfg := &Config{
		Forecast: ForecastConfig{
			InitialPCI: 85,
			DecayRate:  0.05,
		},
	}

	// Per-city override
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

	// No override — inherits top-level
	cityNoOverride := &CityConfig{Name: "Default"}
	fc2 := cfg.ResolvedForecast(cityNoOverride)
	if fc2.InitialPCI != 85 {
		t.Errorf("expected inherited InitialPCI 85, got %f", fc2.InitialPCI)
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
