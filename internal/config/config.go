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

	"github.com/jcrussell/solvent-streets/internal/units"
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
	// DefaultMinHexArea is the minimum projected hex area (in square
	// meters) below which a clipped boundary sliver is dropped from the
	// heatmap. A single feature inside a tiny clipped hex would render as
	// 100%-covered and visually misrepresent the edge. Cities with smaller
	// hex_edge_m may want to tune this via display.min_hex_area.
	DefaultMinHexArea = 100.0
	// DefaultCoordinateDecimals is the precision of [lon, lat] floats
	// emitted in hex GeoJSON. 6 decimals ≈ 11 cm, plenty for a
	// city-scale heatmap; the JTS reproject path used to hardcode 7
	// (~1 cm). Lower precision shrinks per-city JSON by 30-50%. Cities
	// or examples that genuinely need finer resolution can override via
	// export.coordinate_decimals.
	DefaultCoordinateDecimals = 6
)

// Sentinels for failure modes that warrant a remediation hint at the call
// site. Declared here (not in cmdutil) to avoid an import cycle: cmdutil
// imports config. Callers detect these with errors.Is and wrap with
// cmdutil.Hintf before returning to the runner.
var (
	ErrConfigNotFound = errors.New("pvmt.toml not found (searched from working directory to root)")
	ErrNoCities       = errors.New("at least one [[cities]] entry is required")
	// ErrInvalidConfig marks the chain returned by Config.Validate. The
	// runner detects it (via errors.Is) and maps the failure to exit
	// code 2 by wrapping in cmdutil.FlagError at the cmdutil/factory
	// boundary -- internal/config cannot import cmdutil without a cycle.
	ErrInvalidConfig = errors.New("invalid config")
)

type Config struct {
	Grid     GridConfig     `toml:"grid"`
	Display  DisplayConfig  `toml:"display"`
	Layers   []LayerConfig  `toml:"layers"`
	Export   ExportConfig   `toml:"export"`
	Forecast ForecastConfig `toml:"forecast"`
	Cities   []CityConfig   `toml:"cities"`

	// ConfigID is the opaque identifier used as part of the cities
	// table key (UNIQUE(slug, config_id)). Optional in TOML; when
	// absent it is filled at load time with the 16-char sha256 prefix
	// of the absolute path the config was loaded from (Load) or of
	// the fs.FS-relative name (LoadFS). Users who want a key that is
	// stable across repo moves, renames, or cross-machine sharing set
	// it explicitly. Distinct values disambiguate two configs that
	// happen to define the same city slug (the original solvent-streets-zqul
	// case: two examples both defining "Austin").
	ConfigID string `toml:"config_id,omitempty"`

	// SourcePath is the filesystem path of the loaded pvmt.toml file.
	// Set programmatically by Load/FindAndLoad, not a TOML field.
	// Used by export/server to read the raw TOML back for the "Config"
	// tab; not used as a database key (see ConfigID).
	SourcePath string `toml:"-"`

	// contentHash is the SHA-256 prefix of the raw TOML bytes the
	// config was loaded from. Set by parseConfig; empty when the
	// Config is constructed in-memory (tests). Read via Hash() — see
	// internal/config/hash.go for the rationale (path-independent,
	// stable across Config struct evolution).
	contentHash string `toml:"-"`
}

type DisplayConfig struct {
	Units string `toml:"units"` // "metric" or "imperial" (default: "imperial")
	// MinHexArea drops boundary-sliver hexes from the heatmap (see
	// DefaultMinHexArea). Resolve via Config.MinHexArea(); a zero
	// or unset value falls back to the default, which is what most cities
	// want. Negative values are rejected at config load.
	MinHexArea float64 `toml:"min_hex_area"`
}

// MinHexArea returns the effective heatmap sliver threshold in square
// meters: the configured value if positive, else DefaultMinHexArea.
func (c *Config) MinHexArea() float64 {
	if c.Display.MinHexArea > 0 {
		return c.Display.MinHexArea
	}
	return DefaultMinHexArea
}

// UnitSystem returns the resolved display unit system. Precedence:
// PVMT_UNITS env > Display.Units file > imperial. Callers that need the
// source label (including --units flag support) use resolveUnits
// directly via Config.Resolve.
func (c *Config) UnitSystem() units.System {
	v, _ := c.resolveUnits("")
	return v
}

type ExportConfig struct {
	Title string `toml:"title"`
	// CoordinateDecimals sets the precision of emitted hex GeoJSON
	// coordinates. Resolve via Config.CoordinateDecimals(); zero or
	// unset falls back to DefaultCoordinateDecimals.
	CoordinateDecimals int `toml:"coordinate_decimals"`
}

// CoordinateDecimals returns the effective hex GeoJSON coordinate
// precision: the configured value if positive, else
// DefaultCoordinateDecimals.
func (c *Config) CoordinateDecimals() int {
	if c.Export.CoordinateDecimals > 0 {
		return c.Export.CoordinateDecimals
	}
	return DefaultCoordinateDecimals
}

type ForecastConfig struct {
	InitialPCI float64       `toml:"initial_pci"` // starting PCI (1-100); 0 means "use default 85"
	DecayRate  float64       `toml:"decay_rate"`  // PCI decay k-value, 0 = use class defaults
	GrowthRate float64       `toml:"growth_rate"` // annual pavement growth rate (0.01 = 1%)
	Years      int           `toml:"years"`       // forecast horizon, default 20
	CostTiers  []CostTierCfg `toml:"cost_tiers"`  // custom cost tiers
	// CurrentBudget is the city's annual pavement-repair budget in
	// dollars, used by the solvency export (insolvency year, break-even,
	// funding gap). A value type, NOT *float64: a pointer breaks the
	// deterministic Config.Hash() reflection fallback (hash.go). 0 means
	// "not provided" — no real city budgets a literal $0 — and disables
	// the budget-dependent metrics for that city. Must be a cited figure
	// in shipped examples (the site makes dollar claims about named cities).
	CurrentBudget float64 `toml:"current_budget,omitempty"`
}

// Validate rejects obviously-wrong forecast inputs at config-load time
// per byob-input-validation.2. Zero values are allowed for fields whose
// comment says "0 means default" (InitialPCI, DecayRate, Years);
// NormalizeForecast fills those in. GrowthRate has no default sentinel
// so 0 is a legitimate "no growth" value.
func (fc *ForecastConfig) Validate() error {
	if fc.InitialPCI < 0 || fc.InitialPCI > 100 {
		return fmt.Errorf("forecast.initial_pci %g out of range (0-100)", fc.InitialPCI)
	}
	if fc.DecayRate < 0 || fc.DecayRate > 1 {
		return fmt.Errorf("forecast.decay_rate %g out of range (0-1)", fc.DecayRate)
	}
	if fc.GrowthRate < -0.5 || fc.GrowthRate > 0.5 {
		return fmt.Errorf("forecast.growth_rate %g out of range (-0.5 to 0.5)", fc.GrowthRate)
	}
	if fc.Years < 0 {
		return fmt.Errorf("forecast.years %d must be non-negative", fc.Years)
	}
	if fc.CurrentBudget < 0 {
		return fmt.Errorf("forecast.current_budget %g must be non-negative", fc.CurrentBudget)
	}
	// Custom cost_tiers feed buildAnchors' midpoint interpolation directly, so
	// a negative cost, an inverted band, or an out-of-range bound silently
	// produces wrong dollar figures. Reject the degenerate cases (overlap and
	// coverage gaps are intentionally left to the operator).
	for i, t := range fc.CostTiers {
		if t.CostPerSqM <= 0 {
			return fmt.Errorf("forecast.cost_tiers[%d] (%s): cost_per_sqm %g must be positive", i, t.Label, t.CostPerSqM)
		}
		if t.MinPCI < 0 || t.MaxPCI > 101 {
			return fmt.Errorf("forecast.cost_tiers[%d] (%s): pci band [%g, %g) out of range (0-101)", i, t.Label, t.MinPCI, t.MaxPCI)
		}
		if t.MinPCI >= t.MaxPCI {
			return fmt.Errorf("forecast.cost_tiers[%d] (%s): min_pci %g must be less than max_pci %g", i, t.Label, t.MinPCI, t.MaxPCI)
		}
		if t.Label == "" {
			return fmt.Errorf("forecast.cost_tiers[%d]: label must not be empty", i)
		}
	}
	return nil
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
	// BoundaryRelationID is the OSM relation ID for the city's admin
	// boundary (admin_level typically 8). Set this for cities whose
	// boundary is reachable only via Overpass — e.g. Albuquerque, NM
	// (relation 171262), where OSM models the city as a place=city
	// node and Nominatim's index doesn't surface the admin boundary.
	// When unset (the common case), the ingest path uses Nominatim
	// search by Name. See solvent-streets-95i8 for the diagnosis
	// pattern.
	BoundaryRelationID int64 `toml:"boundary_relation_id"`

	// AllowPrivateArcGIS opts this city's ArcGISURL out of the
	// loopback / link-local / private-IP defense (solvent-streets-di49).
	// Required only for staging or self-hosted ArcGIS endpoints on
	// internal networks; public ArcGIS endpoints work without it.
	AllowPrivateArcGIS bool `toml:"allow_private_arcgis"`
}

type GridConfig struct {
	HexEdgeM float64 `toml:"hex_edge_m"`
}

// HexEdge returns the top-level grid hex-edge length in meters.
// Precedence: PVMT_HEX_EDGE_M env > Grid.HexEdgeM file > DefaultHexEdgeM.
// Invalid or non-positive env values are ignored; the warnInvalidEnv
// middleware surfaces them to the user.
func (c *Config) HexEdge() float64 {
	v, _ := c.resolveHexEdge()
	return v
}

// ResolvedHexEdge returns the hex edge for a city: per-city override if
// set, else the top-level resolution.
func (c *Config) ResolvedHexEdge(city *CityConfig) float64 {
	v, _ := c.resolveHexEdgeForCity(city)
	return v
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
	fc, _ := c.resolveForecast(city)
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
// should use Load.
//
// SourcePath is set to name (the fs-relative name). ConfigID is filled
// from a hash of name when the TOML did not set it explicitly — this
// is deterministic but not meaningful, since LoadFS is test-only and
// the fs-relative name is not a real filesystem path.
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
	if cfg.ConfigID == "" {
		cfg.ConfigID = hashBytes([]byte(name))
	}
	return cfg, nil
}

// Load reads a pvmt.toml config from the given filesystem path.
//
// SourcePath is the caller-supplied path verbatim (used by export/server
// to read the raw TOML back).
//
// ConfigID is filled from the 16-char sha256 prefix of the absolute form
// of path when the TOML did not set it explicitly. Absolutizing the path
// before hashing means two callers that point at the same file with
// different spellings (gensite's relative-path glob and `pvmt all`'s
// absolute-path FindAndLoad) produce the same ConfigID, so they reach
// the same cities row.
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
	if cfg.ConfigID == "" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve config path %q: %w", path, err)
		}
		cfg.ConfigID = hashBytes([]byte(abs))
	}
	return cfg, nil
}

// parseConfig decodes and validates TOML bytes into a Config. Shared
// between Load and LoadFS.
//
// Unknown TOML keys are rejected per byob-input-validation.2: a typo
// in a key name would otherwise unmarshal as the zero value and
// silently produce a degenerate config. Using toml.Decode lets us
// inspect MetaData.Undecoded() to surface the typo at load time
// instead of hours later in production.
func parseConfig(data []byte) (*Config, error) {
	var cfg Config
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return nil, errors.Join(ErrInvalidConfig,
			fmt.Errorf("unknown config key(s): %s", strings.Join(keys, ", ")))
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.contentHash = hashBytes(data)
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

// Validate rejects a parsed Config that violates a shape invariant before
// any consumer trusts its values. Errors are joined under ErrInvalidConfig
// so the cmdutil boundary can wrap them in FlagError (exit code 2).
// Note: hex-edge values of 0 are explicitly allowed; HexEdge() falls back
// to DefaultHexEdgeM. Only negative values are rejected.
func (c *Config) Validate() error {
	if len(c.Cities) == 0 {
		return errors.Join(ErrInvalidConfig, ErrNoCities)
	}
	if c.Grid.HexEdgeM < 0 {
		return errors.Join(ErrInvalidConfig,
			fmt.Errorf("grid.hex_edge_m %g must be non-negative", c.Grid.HexEdgeM))
	}
	if c.Display.MinHexArea < 0 {
		return errors.Join(ErrInvalidConfig,
			fmt.Errorf("display.min_hex_area %g must be non-negative", c.Display.MinHexArea))
	}
	if err := c.Forecast.Validate(); err != nil {
		return errors.Join(ErrInvalidConfig, err)
	}
	seen := make(map[string]bool)
	for i, city := range c.Cities {
		if city.Name == "" {
			return errors.Join(ErrInvalidConfig, fmt.Errorf("cities[%d].name is required", i))
		}
		slug := city.Slug()
		if slug == "" {
			return errors.Join(ErrInvalidConfig,
				fmt.Errorf("cities[%d].name %q produces empty slug", i, city.Name))
		}
		if seen[slug] {
			return errors.Join(ErrInvalidConfig,
				fmt.Errorf("duplicate city name %q (slug: %s)", city.Name, slug))
		}
		if city.HexEdgeM < 0 {
			return errors.Join(ErrInvalidConfig,
				fmt.Errorf("cities[%d] (%s): hex_edge_m %g must be non-negative", i, city.Name, city.HexEdgeM))
		}
		if city.BoundaryRelationID < 0 {
			return errors.Join(ErrInvalidConfig,
				fmt.Errorf("cities[%d] (%s): boundary_relation_id %d must be non-negative", i, city.Name, city.BoundaryRelationID))
		}
		if city.Forecast != nil {
			if err := city.Forecast.Validate(); err != nil {
				return errors.Join(ErrInvalidConfig,
					fmt.Errorf("cities[%d] (%s): %w", i, city.Name, err))
			}
		}
		seen[slug] = true
	}
	return nil
}
