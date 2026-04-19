package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"pvmt/internal/units"
)

// Default values applied by ResolvedForecast / NormalizeForecast / HexEdge
// when the corresponding field is unset (zero) or out of range. Exported so
// callers that build config views outside ResolvedForecast (e.g.
// export.ResolvedTOML) can apply the same rules without re-hardcoding
// the numbers.
const (
	DefaultHexEdgeM      = 100.0
	DefaultInitialPCI    = 85.0
	DefaultForecastYears = 20
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

// UnitSystem returns the resolved display unit system. Precedence:
// PVMT_UNITS env > Display.Units file > imperial.
func (c *Config) UnitSystem() units.System {
	if v, ok := os.LookupEnv("PVMT_UNITS"); ok && v != "" {
		return units.ParseSystem(v)
	}
	if c.Display.Units != "" {
		return units.ParseSystem(c.Display.Units)
	}
	return units.Imperial
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

// HexEdge returns the top-level grid hex-edge length in meters.
// Precedence: PVMT_HEX_EDGE_M env > Grid.HexEdgeM file > DefaultHexEdgeM.
// Invalid or non-positive env values are ignored; the warnInvalidEnv
// middleware surfaces them to the user.
func (c *Config) HexEdge() float64 {
	if v, ok := os.LookupEnv("PVMT_HEX_EDGE_M"); ok && v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	if c.Grid.HexEdgeM > 0 {
		return c.Grid.HexEdgeM
	}
	return DefaultHexEdgeM
}

// ResolvedHexEdge returns the hex edge for a city: per-city override if
// set, else the top-level resolution.
func (c *Config) ResolvedHexEdge(city *CityConfig) float64 {
	if city.HexEdgeM > 0 {
		return city.HexEdgeM
	}
	return c.HexEdge()
}

// ResolvedForecast returns the forecast config for city with all layers
// merged (env > per-city > top-level) and defaults applied. Every field
// that NormalizeForecast owns (InitialPCI, Years) is guaranteed valid on
// the returned value — callers read them directly. A nil city is treated
// as "no per-city overrides."
//
// Invalid or out-of-range env values are silently ignored so the merged
// config still reflects the next layer down; the warnInvalidEnv
// middleware at the CLI boundary surfaces these to the user.
func (c *Config) ResolvedForecast(city *CityConfig) ForecastConfig {
	fc := c.Forecast
	if city != nil {
		applyCityForecast(&fc, city.Forecast)
	}
	applyForecastEnv(&fc)
	NormalizeForecast(&fc)
	return fc
}

// NormalizeForecast fills unset or out-of-range InitialPCI and Years with
// their package defaults. DecayRate, GrowthRate, and CostTiers carry
// forecast-package defaults that config can't know about and are left to
// the caller. Idempotent.
func NormalizeForecast(fc *ForecastConfig) {
	if fc.InitialPCI <= 0 || fc.InitialPCI > 100 {
		fc.InitialPCI = DefaultInitialPCI
	}
	if fc.Years <= 0 {
		fc.Years = DefaultForecastYears
	}
}

// applyCityForecast overlays a per-city forecast block onto fc in place.
// Each non-zero field wins over the top-level value. A nil override is a
// no-op.
func applyCityForecast(fc, override *ForecastConfig) {
	if override == nil {
		return
	}
	if override.InitialPCI > 0 && override.InitialPCI <= 100 {
		fc.InitialPCI = override.InitialPCI
	}
	if override.DecayRate > 0 {
		fc.DecayRate = override.DecayRate
	}
	if override.GrowthRate > 0 {
		fc.GrowthRate = override.GrowthRate
	}
	if override.Years > 0 {
		fc.Years = override.Years
	}
	if len(override.CostTiers) > 0 {
		fc.CostTiers = override.CostTiers
	}
}

// applyForecastEnv overlays PVMT_FORECAST_* env overrides onto fc in
// place. Invalid or out-of-range values are ignored here; the
// warnInvalidEnv middleware surfaces them to the user instead.
func applyForecastEnv(fc *ForecastConfig) {
	if n, ok := parsePositiveIntEnv("PVMT_FORECAST_YEARS"); ok {
		fc.Years = n
	}
	if f, ok := parsePCIEnv("PVMT_FORECAST_INITIAL_PCI"); ok {
		fc.InitialPCI = f
	}
}

// parsePositiveIntEnv returns the int value of envKey if it is set to a
// string that parses as a positive integer, else (0, false).
func parsePositiveIntEnv(envKey string) (int, bool) {
	s, ok := os.LookupEnv(envKey)
	if !ok || s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// parsePCIEnv returns the float value of envKey if it is set to a
// string that parses as a float in (0, 100], else (0, false).
func parsePCIEnv(envKey string) (float64, bool) {
	s, ok := os.LookupEnv(envKey)
	if !ok || s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 || f > 100 {
		return 0, false
	}
	return f, true
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

// LoadFS reads a pvmt.toml config from fsys at the given name. This is
// the fs.FS-oriented entrypoint for hermetic tests; production callers
// that want an absolute-path SourcePath should use Load. SourcePath is
// set to name (the fs-relative name) by default.
func LoadFS(fsys fs.FS, name string) (*Config, error) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg, err := parseConfig(data)
	if err != nil {
		return nil, err
	}
	cfg.SourcePath = name
	return cfg, nil
}

// Load reads a pvmt.toml config from the given filesystem path.
// SourcePath on the returned Config is set to path (the absolute/relative
// filesystem path the caller passed). For hermetic fs.FS-based loading,
// use LoadFS.
//
// Load uses os.ReadFile directly (rather than delegating to LoadFS) so
// the read error carries the full path the caller supplied — users who
// mistype --config want to see which path failed, not just the basename.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg, err := parseConfig(data)
	if err != nil {
		return nil, err
	}
	cfg.SourcePath = path
	return cfg, nil
}

// parseConfig decodes and validates TOML bytes into a Config. Shared
// between Load and LoadFS.
func parseConfig(data []byte) (*Config, error) {
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
