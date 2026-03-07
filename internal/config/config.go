package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Project ProjectConfig `toml:"project"`
	Area    AreaConfig    `toml:"area"`
	Sources SourcesConfig `toml:"sources"`
	Grid    GridConfig    `toml:"grid"`
	Layers   []LayerConfig  `toml:"layers"`
	Export   ExportConfig   `toml:"export"`
	Forecast ForecastConfig `toml:"forecast"`
}

type ExportConfig struct {
	OutputDir   string `toml:"output_dir"`
	Title       string `toml:"title"`
	Description string `toml:"description"`
}

type ForecastConfig struct {
	DecayRate        float64       `toml:"decay_rate"`         // PCI decay k-value, 0 = use class defaults
	GrowthRate       float64       `toml:"growth_rate"`        // annual pavement growth rate (0.01 = 1%)
	Years            int           `toml:"years"`              // forecast horizon, default 20
	CostTiers        []CostTierCfg `toml:"cost_tiers"`         // custom cost tiers
}

type CostTierCfg struct {
	MinPCI      float64 `toml:"min_pci"`
	MaxPCI      float64 `toml:"max_pci"`
	CostPerSqFt float64 `toml:"cost_per_sqft"`
	Label       string  `toml:"label"`
}

// ForecastYears returns the configured forecast horizon, defaulting to 20.
func (c *Config) ForecastYears() int {
	if c.Forecast.Years > 0 {
		return c.Forecast.Years
	}
	return 20
}

type LayerConfig struct {
	Name   string `toml:"name"`
	Type   string `toml:"type"`   // "csv" or "geojson"
	Path   string `toml:"path"`   // file path
	IDProp string `toml:"id_prop"` // property/column for ID
}

type ProjectConfig struct {
	Name string `toml:"name"`
}

type AreaConfig struct {
	// BBox is [south, west, north, east] in WGS84 degrees.
	BBox [4]float64 `toml:"bbox"`
}

type SourcesConfig struct {
	Overpass  bool   `toml:"overpass"`
	ArcGISURL string `toml:"arcgis_url"`
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

// Center returns the center of the bounding box as (lon, lat).
func (c *Config) Center() (float64, float64) {
	lat := (c.Area.BBox[0] + c.Area.BBox[2]) / 2
	lon := (c.Area.BBox[1] + c.Area.BBox[3]) / 2
	return lon, lat
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
	return nil, fmt.Errorf("pvmt.toml not found (searched from working directory to root)")
}

func (c *Config) validate() error {
	if c.Project.Name == "" {
		return fmt.Errorf("project.name is required")
	}
	bbox := c.Area.BBox
	if bbox[0] >= bbox[2] {
		return fmt.Errorf("area.bbox: south (%f) must be less than north (%f)", bbox[0], bbox[2])
	}
	if bbox[1] >= bbox[3] {
		return fmt.Errorf("area.bbox: west (%f) must be less than east (%f)", bbox[1], bbox[3])
	}
	if bbox[0] < -90 || bbox[2] > 90 {
		return fmt.Errorf("area.bbox: latitude must be between -90 and 90")
	}
	if bbox[1] < -180 || bbox[3] > 180 {
		return fmt.Errorf("area.bbox: longitude must be between -180 and 180")
	}
	return nil
}
