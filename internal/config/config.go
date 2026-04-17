package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"

	"pvmt/internal/units"
)

// Sentinels for failure modes that warrant a remediation hint at the call
// site. Declared here (not in cmdutil) to avoid an import cycle: cmdutil
// imports config. Callers detect these with errors.Is and wrap with
// cmdutil.Hintf before returning to the runner.
var (
	ErrConfigNotFound = errors.New("pvmt.toml not found (searched from working directory to root)")
	ErrNoCities       = errors.New("at least one [[cities]] entry is required")
)

type Config struct {
	Grid     GridConfig     `toml:"grid"`
	Display  DisplayConfig  `toml:"display"`
	Layers   []LayerConfig  `toml:"layers"`
	Export   ExportConfig   `toml:"export"`
	Forecast ForecastConfig `toml:"forecast"`
	Cities   []CityConfig   `toml:"cities"`

	// SourcePath is the filesystem path of the loaded pvmt.toml file.
	// Set programmatically by Load/FindAndLoad, not a TOML field.
	SourcePath string `toml:"-"`
}

type DisplayConfig struct {
	Units string `toml:"units"` // "metric" or "imperial" (default: "imperial")
}

// UnitSystem returns the configured display unit system.
func (c *Config) UnitSystem() units.System {
	return units.ParseSystem(c.Display.Units)
}

type ExportConfig struct {
	OutputDir   string `toml:"output_dir"`
	Title       string `toml:"title"`
	Description string `toml:"description"`
}

type ForecastConfig struct {
	InitialPCI float64       `toml:"initial_pci"` // starting PCI (1-100); 0 means "use default 85"
	DecayRate  float64       `toml:"decay_rate"`  // PCI decay k-value, 0 = use class defaults
	GrowthRate float64       `toml:"growth_rate"` // annual pavement growth rate (0.01 = 1%)
	Years      int           `toml:"years"`       // forecast horizon, default 20
	CostTiers  []CostTierCfg `toml:"cost_tiers"`  // custom cost tiers
}

type CostTierCfg struct {
	MinPCI     float64 `toml:"min_pci"`
	MaxPCI     float64 `toml:"max_pci"`
	CostPerSqM float64 `toml:"cost_per_sqm"`
	Label      string  `toml:"label"`
}

// ForecastYears returns the configured forecast horizon, defaulting to 20.
func (c *Config) ForecastYears() int {
	return c.Forecast.ResolvedYears()
}

// ResolvedInitialPCI returns the initial PCI, defaulting to 85 if not set.
func (fc *ForecastConfig) ResolvedInitialPCI() float64 {
	if fc.InitialPCI > 0 && fc.InitialPCI <= 100 {
		return fc.InitialPCI
	}
	return 85.0
}

// ResolvedYears returns the forecast horizon, defaulting to 20 if not set.
func (fc *ForecastConfig) ResolvedYears() int {
	if fc.Years > 0 {
		return fc.Years
	}
	return 20
}

type LayerConfig struct {
	Name   string `toml:"name"`
	Type   string `toml:"type"`    // "csv" or "geojson"
	Path   string `toml:"path"`    // file path
	IDProp string `toml:"id_prop"` // property/column for ID
}

type CityConfig struct {
	Name      string          `toml:"name"`
	Overpass  bool            `toml:"overpass"`
	ArcGISURL string          `toml:"arcgis_url"`
	HexEdgeM  float64         `toml:"hex_edge_m"`
	Forecast  *ForecastConfig `toml:"forecast,omitempty"`
}

type GridConfig struct {
	HexEdgeM float64 `toml:"hex_edge_m"`
}

// HexEdge returns the configured hex edge length in meters, defaulting to 100.
func (c *Config) HexEdge() float64 {
	if c.Grid.HexEdgeM > 0 {
		return c.Grid.HexEdgeM
	}
	return 100
}

// ResolvedHexEdge returns the hex edge for a city, using per-city override if set.
func (c *Config) ResolvedHexEdge(city *CityConfig) float64 {
	if city.HexEdgeM > 0 {
		return city.HexEdgeM
	}
	return c.HexEdge()
}

// ResolvedForecast merges top-level forecast defaults with per-city overrides.
func (c *Config) ResolvedForecast(city *CityConfig) ForecastConfig {
	if city.Forecast == nil {
		return c.Forecast
	}
	fc := c.Forecast
	if city.Forecast.InitialPCI > 0 && city.Forecast.InitialPCI <= 100 {
		fc.InitialPCI = city.Forecast.InitialPCI
	}
	if city.Forecast.DecayRate > 0 {
		fc.DecayRate = city.Forecast.DecayRate
	}
	if city.Forecast.GrowthRate > 0 {
		fc.GrowthRate = city.Forecast.GrowthRate
	}
	if city.Forecast.Years > 0 {
		fc.Years = city.Forecast.Years
	}
	if len(city.Forecast.CostTiers) > 0 {
		fc.CostTiers = city.Forecast.CostTiers
	}
	return fc
}

// Slug returns a URL-safe slug for the city name.
func (c *CityConfig) Slug() string {
	return Slugify(c.Name)
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify converts a name to a URL-safe slug (lowercase, non-alnum → -, collapse, trim).
func Slugify(name string) string {
	s := strings.ToLower(name)
	s = nonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

// Load reads a pvmt.toml config file from the given path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	cfg.SourcePath = path
	return &cfg, nil
}

// FindAndLoad searches for pvmt.toml starting from dir, walking up to root.
func FindAndLoad(dir string) (*Config, error) {
	for {
		path := filepath.Join(dir, "pvmt.toml")
		if _, err := os.Stat(path); err == nil {
			return Load(path)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return nil, ErrConfigNotFound
}

func (c *Config) validate() error {
	if len(c.Cities) == 0 {
		return ErrNoCities
	}
	seen := make(map[string]bool)
	for i, city := range c.Cities {
		if city.Name == "" {
			return fmt.Errorf("cities[%d].name is required", i)
		}
		slug := city.Slug()
		if slug == "" {
			return fmt.Errorf("cities[%d].name %q produces empty slug", i, city.Name)
		}
		if seen[slug] {
			return fmt.Errorf("duplicate city name %q (slug: %s)", city.Name, slug)
		}
		seen[slug] = true
	}
	return nil
}
