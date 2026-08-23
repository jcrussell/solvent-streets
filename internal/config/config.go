package config

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
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
	// emitted in exported GeoJSON (the hex grid and the display boundary).
	//
	// 5 decimals is ~1.1 m of latitude and ~0.79 m of longitude at mid
	// latitudes, against a hex edge of 80-300 m — between two and three orders
	// of magnitude below anything the heatmap can express. It costs a digit per
	// coordinate across the two largest files in the export.
	//
	// This does NOT open gaps between adjacent hexes: neighbours share vertices
	// computed from the same col*colSpacing arithmetic, so identical inputs
	// round identically and the shared edge stays shared. (The JTS reproject
	// path used to hardcode 7, ~1 cm, which was never meaningful here.)
	//
	// Raise it via export.coordinate_decimals if a downstream consumer needs
	// finer resolution than the display does.
	DefaultCoordinateDecimals = 5
	// DefaultBoundarySimplifyM is the Ramer-Douglas-Peucker tolerance, in
	// meters, applied to the DISPLAY copy of the city boundary before it is
	// written to boundary.geojson (or served from /data/boundary.geojson).
	// Nominatim boundaries carry far more vertices than a dashed outline at
	// city zoom can resolve — Jacksonville alone is 205,961 coordinate pairs
	// across 7,371 rings — and 10 m retains ~23.5% of them for a ~0.15%
	// change in enclosed area.
	//
	// This never touches the authoritative boundary: hex clipping
	// (export.clipHexGridToBoundary) and the area figures behind
	// meta.json's CityArea/PctPaved read store.GetBoundary directly.
	DefaultBoundarySimplifyM = 10.0
	// DefaultCostOverhead is 1.0 — no multiplier — and that is a calibrated
	// choice, not an oversight. Read docs/validation.md §6 before changing it.
	//
	// The cost tiers are BARE construction prices, and a per-treatment
	// comparison (§3) does show them ~2-3x under Berkeley's loaded StreetSaver
	// schedule, which invites exactly the mistake of loading them by ~1.5x
	// (+20% ADA, +15% soft costs, +10% contingency). But the system output is
	// already loaded-equivalent: bare tiers x the §4 condition spread x the
	// N=12 treatment cycle reproduce Berkeley's cited real hold-steady spend of
	// $5.6/m2-yr, and that cited figure is itself a loaded municipal number.
	//
	// Measured against the committed backtest fixtures:
	//   overhead 1.0 -> Berkeley break_even $17.9M ($5.42/m2-yr) vs cited $18.3M
	//   overhead 1.5 -> Berkeley break_even $26.8M ($8.13/m2-yr), +47% over
	// The second also breaks backtest_test.go's [$4.5, $6.5]/m2-yr calibration
	// bracket. The overhead the tiers appear to be missing is already absorbed
	// by the cycle and the spread; applying it again double-counts it.
	//
	// The knob exists so the regime is EXPLICIT and adjustable — a city whose
	// own numbers say otherwise sets it, and the Financials tab exposes it as a
	// live slider — not because the shipped default needs it.
	DefaultCostOverhead = 1.0
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
	Export   ExportConfig   `toml:"export"`
	Forecast ForecastConfig `toml:"forecast"`
	Cities   []CityConfig   `toml:"cities"`

	// Include pulls cities from other pvmt.toml files into this one, tagging
	// each pulled-in city at the include site. A city present in several
	// includes is merged once with the union of their tags. Resolved by Load
	// (relative to this file's directory); see include.go. When Include is
	// non-empty the file may omit [[cities]] of its own — the ≥1-city
	// requirement is enforced after the merge.
	Include []IncludeSpec `toml:"include,omitempty"`

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

	// loadWarnings holds the non-fatal notices raised while resolving
	// [[include]] blocks — today, one per city whose calibration was
	// superseded by an earlier include under first-include-wins (see
	// include.go). Set by Load; read via LoadWarnings() and printed by
	// the warnConfigLoad middleware, since this package has no IOStreams.
	//
	// Unexported deliberately: Config.Hash() falls back to a TOML
	// re-encode of the struct for in-memory configs, and the encoder
	// skips unexported fields — so warnings cannot perturb a snapshot
	// hash. Same reason contentHash is unexported. See
	// TestLoadWarnings_DoNotAffectHash.
	loadWarnings []string `toml:"-"`
}

// LoadWarnings returns the non-fatal notices collected while this config was
// loaded, in the order they were raised. Currently these describe per-city
// calibration superseded by an earlier [[include]]: the merge picks the first
// include's numbers, and this is how that choice reaches the user. Empty for a
// config with no includes or no disagreements.
//
// The returned slice is owned by the Config; callers must not mutate it.
func (c *Config) LoadWarnings() []string { return c.loadWarnings }

type DisplayConfig struct {
	Units string `toml:"units"` // "metric" or "imperial" (default: "imperial")
	// MinHexArea drops boundary-sliver hexes from the heatmap (see
	// DefaultMinHexArea). Resolve via Config.MinHexArea(); a zero
	// or unset value falls back to the default, which is what most cities
	// want. Negative values are rejected at config load.
	MinHexArea float64 `toml:"min_hex_area"`
}

// MinHexArea returns the effective top-level heatmap sliver threshold in
// square meters: the configured value if positive, else DefaultMinHexArea.
// Per-city callers want ResolvedMinHexArea.
func (c *Config) MinHexArea() float64 {
	v, _ := c.resolveMinHexArea()
	return v
}

// ResolvedMinHexArea returns the heatmap sliver threshold for a city:
// per-city override if set, else the top-level resolution. Mirrors
// ResolvedHexEdge, to which it is coupled — the threshold is an area in the
// same projected units the hex edge sizes, so a city that tunes hex_edge_m
// usually has to tune min_hex_area with it. There is no env layer (none
// exists for display.min_hex_area), so the chain is city > file > default.
func (c *Config) ResolvedMinHexArea(city *CityConfig) float64 {
	v, _ := c.resolveMinHexAreaForCity(city)
	return v
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
	// CoordinateDecimals sets the precision of emitted GeoJSON
	// coordinates — the hex grid and the display boundary. Resolve via
	// Config.CoordinateDecimals(); zero or unset falls back to
	// DefaultCoordinateDecimals.
	CoordinateDecimals int `toml:"coordinate_decimals"`
	// BoundarySimplifyM is the RDP tolerance in meters for the display
	// boundary. Resolve via Config.BoundarySimplifyM(); zero or unset falls
	// back to DefaultBoundarySimplifyM, and a NEGATIVE value opts out
	// entirely (the stored GeoJSON is emitted byte-for-byte).
	BoundarySimplifyM float64 `toml:"boundary_simplify_m"`
}

// CoordinateDecimals returns the effective GeoJSON coordinate precision:
// the configured value if positive, else DefaultCoordinateDecimals.
func (c *Config) CoordinateDecimals() int {
	if c.Export.CoordinateDecimals > 0 {
		return c.Export.CoordinateDecimals
	}
	return DefaultCoordinateDecimals
}

// BoundarySimplifyM returns the effective display-boundary simplification
// tolerance in meters.
//
// Unlike CoordinateDecimals this keys on zero-vs-nonzero, NOT on positive:
// a negative tolerance is the documented byte-exact opt-out, so it has to
// survive the resolver rather than being folded into the default. Only an
// unset (zero) value falls back.
func (c *Config) BoundarySimplifyM() float64 {
	if c.Export.BoundarySimplifyM != 0 {
		return c.Export.BoundarySimplifyM
	}
	return DefaultBoundarySimplifyM
}

type ForecastConfig struct {
	InitialPCI float64       `toml:"initial_pci"` // starting PCI (1-100); 0 means "use default 85"
	DecayRate  float64       `toml:"decay_rate"`  // PCI decay k-value, 0 = use class defaults
	GrowthRate float64       `toml:"growth_rate"` // annual pavement growth rate (0.01 = 1%)
	Years      int           `toml:"years"`       // forecast horizon, default 20
	CostTiers  []CostTierCfg `toml:"cost_tiers"`  // custom cost tiers
	// TreatmentCycleYears is the pavement treatment cycle N: the model schedules
	// ~1/N of the network for treatment each year, so annual need = full-network
	// cost / N. 0 = use forecast.DefaultTreatmentCycleYears (~12); the default is
	// resolved in the forecast core (ResolveCycleYears), not here, so every
	// construction path gets a safe divisor. Scales break_even by 1/N and drives
	// the insolvency threshold. A value type (not *float64) for the same
	// Config.Hash() reason as CurrentBudget.
	TreatmentCycleYears float64 `toml:"treatment_cycle_years"`
	// CurrentBudget is the city's annual pavement-repair budget in
	// dollars, used by the solvency export (insolvency year, break-even,
	// funding gap). A value type, NOT *float64: a pointer breaks the
	// deterministic Config.Hash() reflection fallback (hash.go). 0 means
	// "not provided" — no real city budgets a literal $0 — and disables
	// the budget-dependent metrics for that city. Must be a cited figure
	// in shipped examples (the site makes dollar claims about named cities).
	CurrentBudget float64 `toml:"current_budget,omitempty"`

	// CostOverhead multiplies the cost tiers to turn BARE construction prices
	// into the LOADED program cost a municipality actually budgets. Berkeley's
	// StreetSaver schedule itemizes the stack as +20% ADA curb-ramp compliance
	// (federal law: touch a street, rebuild the ramps), +15% soft costs
	// (design, inspection, project management) and +10% contingency —
	// 1.20 x 1.15 x 1.10 = 1.518. That stack is what this knob EXPRESSES, but
	// it is NOT the default: DefaultCostOverhead is 1.0, because the shipped
	// tiers and treatment cycle are calibrated against real municipal
	// hold-steady spending and so already absorb it. Applying 1.5 on top
	// double-counts. See DefaultCostOverhead and docs/validation.md §3.
	//
	// This is a separate knob from cost_tiers because the two vary
	// independently: regional construction PRICING belongs in the tiers, which
	// are already per-city, while the overhead stack is policy-driven and
	// roughly structural everywhere. A city that commits its own already-loaded
	// [[forecast.cost_tiers]] schedule should set cost_overhead = 1.0 so the
	// load is not applied twice; examples/los-angeles-ca and
	// examples/greater-boston-ma do exactly that.
	//
	// 0 = unset, resolved to DefaultCostOverhead. Set 1.0 for bare
	// construction pricing. Validate rejects non-positive and absurd values.
	CostOverhead float64 `toml:"cost_overhead,omitempty"`

	// growthRateSet records that growth_rate was written explicitly in the
	// TOML, as opposed to being absent and decoding to 0. GrowthRate is the one
	// field here where 0 is a legitimate value a user might mean — "this city
	// does not grow" — rather than an unset sentinel: initial_pci, decay_rate,
	// years, treatment_cycle_years and current_budget all have no real 0 case,
	// so zero-as-unset is correct for them and they need no presence bit.
	//
	// Without this a per-city `growth_rate = 0` was indistinguishable from an
	// absent one, so a city could not opt out of a positive top-level rate
	// (solvent-streets-r312). Unexported on purpose: it must not decode from
	// TOML, must not appear in Config.Hash's struct-encoding fallback, and must
	// not be emitted by `config show`. It rides along on struct copies, which is
	// exactly what the [[include]] merge does.
	growthRateSet bool
}

// GrowthRateSet reports whether growth_rate was explicitly present in the
// source TOML. Callers that resolve forecast layers need this to tell an
// explicit 0 from an absent field; see growthRateSet.
func (fc *ForecastConfig) GrowthRateSet() bool { return fc != nil && fc.growthRateSet }

// nonFinite reports whether v is NaN or either infinity.
//
// It exists because TOML accepts the `nan` and `inf` float literals
// (BurntSushi/toml v1.6.0, parse.go:509) and every comparison against NaN is
// false, so a bare range check like `v < 0 || v > 100` admits NaN silently and
// the value flows on into the forecast. Pair this with every float bound in
// this package. The failure modes are not cosmetic: a NaN forecast knob makes
// `pvmt forecast` print "Initial PCI: NaN" and every dollar column as NaN with
// exit code 0, and a non-finite grid.hex_edge_m yields a silently empty hex
// layer (see the geo.HexGrid guard).
func nonFinite(v float64) bool { return math.IsNaN(v) || math.IsInf(v, 0) }

// Validate rejects obviously-wrong forecast inputs at config-load time
// per byob-input-validation.2. Zero values are allowed for fields whose
// comment says "0 means default" (InitialPCI, DecayRate, Years);
// NormalizeForecast fills those in. GrowthRate has no default sentinel
// so 0 is a legitimate "no growth" value.
//
// Every float check is paired with nonFinite: a bare range comparison admits
// NaN, because all comparisons against NaN are false. cost_overhead's check is
// the exception that needs no pairing — its `!(x > 0 && x <= 5)` form already
// rejects NaN.
func (fc *ForecastConfig) Validate() error {
	if nonFinite(fc.InitialPCI) || fc.InitialPCI < 0 || fc.InitialPCI > 100 {
		return fmt.Errorf("forecast.initial_pci %g out of range (0-100)", fc.InitialPCI)
	}
	if nonFinite(fc.DecayRate) || fc.DecayRate < 0 || fc.DecayRate > 1 {
		return fmt.Errorf("forecast.decay_rate %g out of range (0-1)", fc.DecayRate)
	}
	if nonFinite(fc.GrowthRate) || fc.GrowthRate < -0.5 || fc.GrowthRate > 0.5 {
		return fmt.Errorf("forecast.growth_rate %g out of range (-0.5 to 0.5)", fc.GrowthRate)
	}
	if fc.Years < 0 {
		return fmt.Errorf("forecast.years %d must be non-negative", fc.Years)
	}
	if nonFinite(fc.CurrentBudget) || fc.CurrentBudget < 0 {
		return fmt.Errorf("forecast.current_budget %g must be a non-negative finite number", fc.CurrentBudget)
	}
	// 0 means "use DefaultCostOverhead". A negative or NaN multiplier would
	// produce negative or blank dollar figures with nothing to flag them, and a
	// value above 5 is past any documented load stack (Berkeley's is ~1.5, and
	// its note about small systems running 25-50% higher tops out well under 3)
	// — far more likely a decimal slip or a value typed as a percentage.
	if fc.CostOverhead != 0 && !(fc.CostOverhead > 0 && fc.CostOverhead <= 5) {
		return fmt.Errorf("forecast.cost_overhead %g out of range (0 = default, else 0-5)", fc.CostOverhead)
	}
	// 0 means "use default"; a positive but sub-annual cycle (<1) would make the
	// 1/N gating exceed the full network, and an implausibly long cycle (>40 yr)
	// is almost certainly a typo. The forecast core resolves 0 to the default.
	if nonFinite(fc.TreatmentCycleYears) ||
		(fc.TreatmentCycleYears != 0 && (fc.TreatmentCycleYears < 1 || fc.TreatmentCycleYears > 40)) {
		return fmt.Errorf("forecast.treatment_cycle_years %g out of range (1-40, or 0 for default)", fc.TreatmentCycleYears)
	}
	return validateCostTiers(fc.CostTiers)
}

// validateCostTiers rejects degenerate custom cost tiers. They feed buildAnchors'
// midpoint interpolation directly, so a negative cost, an inverted band, or an
// out-of-range bound silently produces wrong dollar figures (overlap and coverage
// gaps are intentionally left to the operator). Split out of Validate to keep its
// cognitive complexity in check.
//
// All three floats are nonFinite-guarded for the same reason every other float
// bound in this package is: NaN is false against every ordered comparison here,
// so `cost_per_sqm <= 0`, `min_pci < 0`, `max_pci > 101` and `min_pci >= max_pci`
// all admit it. A NaN tier price loads clean and reaches the seed, where
// json.Marshal kills `pvmt export` with "unsupported value: NaN" — after the
// user has already run ingest and compute.
func validateCostTiers(tiers []CostTierCfg) error {
	for i, t := range tiers {
		if nonFinite(t.CostPerSqM) || t.CostPerSqM <= 0 {
			return fmt.Errorf("forecast.cost_tiers[%d] (%s): cost_per_sqm %g must be a positive finite number", i, t.Label, t.CostPerSqM)
		}
		if nonFinite(t.MinPCI) || nonFinite(t.MaxPCI) {
			return fmt.Errorf("forecast.cost_tiers[%d] (%s): pci band [%g, %g) must be finite", i, t.Label, t.MinPCI, t.MaxPCI)
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

type CityConfig struct {
	Name      string  `toml:"name"`
	Overpass  bool    `toml:"overpass"`
	ArcGISURL string  `toml:"arcgis_url"`
	HexEdgeM  float64 `toml:"hex_edge_m"`
	// MinHexArea overrides [display].min_hex_area for this city (square
	// meters; see DefaultMinHexArea). It exists because the threshold is
	// coupled to HexEdgeM — a finer grid needs a smaller sliver threshold —
	// and HexEdgeM is per-city. It is flattened onto the city by the
	// [[include]] merge alongside hex_edge_m (see effectiveMinHexArea): an
	// included example's [display].min_hex_area would otherwise be lost,
	// since the including file's top-level tables are the parent's and the
	// child's are discarded. Resolve via Config.ResolvedMinHexArea; zero or
	// unset falls back to the top level, then to DefaultMinHexArea.
	// Negative values are rejected at config load.
	MinHexArea float64         `toml:"min_hex_area"`
	Forecast   *ForecastConfig `toml:"forecast,omitempty"`
	// Tags are optional grouping labels for the city selector and the
	// compare/aggregate scope filter. A city is rendered under an <optgroup>
	// for each of its tags (multi-membership); cities with no tags fall into
	// an ungrouped "Other" set. Tags are usually assigned at the [[include]]
	// site so a city pulled in by several includes (e.g. a Bay Area metro that
	// is also a top-50 city) accumulates the union of their tags. Used to keep
	// large city lists navigable and to scope compare/aggregate to a subset.
	Tags []string `toml:"tags,omitempty"`
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

	// --- Source identity: set by the [[include]] merge, never by a user. ---
	//
	// A city reached through [[include]] belongs to the file that declared it,
	// not to the file that included it. Without these, a union config gets its
	// own cities rows (keyed on ConfigID) and its own snapshot pin (keyed on
	// Hash()), so it cannot see any of the data the source example ingested and
	// computed — which is exactly why `make site` could not run.
	//
	// All three are toml:"-": BurntSushi treats that as skip, so they never
	// appear in a user's file, never round-trip through ResolvedTOML, and never
	// perturb Hash()'s struct-encoding fallback.
	//
	// They are stamped ONLY when still empty, so a city that arrives through
	// nested includes keeps the identity of the file that actually declared it
	// rather than the middle file's.

	// SourceConfigID is the ConfigID of the config that declared this city.
	// Empty means "declared directly by the config holding it".
	SourceConfigID string `toml:"-"`

	// SourceConfigHash is that config's content hash — the value its own
	// `pvmt compute` writes into snapshots.config_hash. It must be computed the
	// same way Load does (hashBlobs over the file and everything it includes),
	// NOT via the included *Config's Hash(), which carries only the bare-bytes
	// hash parseConfig set and would match a different, older snapshot.
	SourceConfigHash string `toml:"-"`

	// SourceHexEdgeM is the hex edge the source config resolves for this city,
	// captured so a union config can detect the one merge outcome that silently
	// corrupts an export: hex ids are derived from the grid, and stored
	// hex_stats are joined to a freshly rebuilt grid BY ID, so a union that
	// resolves a different edge than the snapshot was computed at produces a
	// city with zero features rather than an error.
	SourceHexEdgeM float64 `toml:"-"`
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

// CityConfigID returns the ConfigID that owns city's row in the cities table.
//
// For a city declared directly this is the config's own ConfigID, preserving
// the deliberate divergence documented on Hash(): two unrelated files that
// happen to define the same slug get distinct rows. A city reached through
// [[include]] is not that case — it IS the included file's city — so it keeps
// the source's id and resolves to the same row the source example ingested
// into.
func (c *Config) CityConfigID(city *CityConfig) string {
	if city != nil && city.SourceConfigID != "" {
		return city.SourceConfigID
	}
	return c.ConfigID
}

// CityHash returns the content hash that scopes snapshot reads for city, i.e.
// the hash whose `pvmt compute` run produced the data this city should read.
// Same rule as CityConfigID: an included city reads (and writes) under the
// source config's hash, everything else under this config's own.
func (c *Config) CityHash(city *CityConfig) string {
	if city != nil && city.SourceConfigHash != "" {
		return city.SourceConfigHash
	}
	return c.Hash()
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
	fc.CostOverhead = fc.ResolvedCostOverhead()
}

// ResolvedCostOverhead is the effective loaded-cost multiplier: the configured
// value when positive, else DefaultCostOverhead.
//
// Unlike DecayRate and CostTiers, this default is applied HERE rather than
// downstream in the forecast package. The forecast core treats a zero overhead
// as 1.0 (bare) on purpose — a projector built without going through config,
// as tests and the parity golden do, must not silently acquire the config
// default — so if
// config did not resolve it, an unset config would price at bare and the
// default would never take effect.
func (fc *ForecastConfig) ResolvedCostOverhead() float64 {
	if fc == nil || fc.CostOverhead <= 0 {
		return DefaultCostOverhead
	}
	return fc.CostOverhead
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
//
// The nonFinite guard is not redundant with the range test. strconv.ParseFloat
// accepts "nan", "inf" and "Infinity" with a nil error, and NaN is false against
// both `<= 0` and `> 100` — so PVMT_FORECAST_INITIAL_PCI=nan was ACCEPTED here
// and `pvmt forecast` printed "Initial PCI: NaN" with every dollar column NaN
// and exit code 0. The file layer grew this guard in Validate; the env layer
// needs its own, because it never goes through Validate.
func parsePCIEnv(envKey string) (float64, bool) {
	s, ok := os.LookupEnv(envKey)
	if !ok || s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || nonFinite(f) || f <= 0 || f > 100 {
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
	// Includes resolve against the real filesystem relative to the including
	// file's directory, which the fs.FS-relative LoadFS cannot do. Rather than
	// silently ignore them, reject — include-using configs must go through Load.
	if len(cfg.Include) > 0 {
		return nil, errors.Join(ErrInvalidConfig,
			errors.New("[[include]] is not supported via LoadFS; load the config from a real path with Load"))
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
// different spellings (a relative path from `make site` and `pvmt all`'s
// absolute-path FindAndLoad) produce the same ConfigID, so they reach
// the same cities row.
//
// Load uses os.ReadFile directly (rather than delegating to LoadFS) so
// the read error carries the full path the caller supplied — users who
// mistype --config want to see which path failed, not just the basename.
func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path %q: %w", path, err)
	}
	warn := &calibrationWarnings{}
	cfg, blobs, err := loadResolved(abs, map[string]bool{}, 0, map[string]*resolvedInclude{}, warn)
	if err != nil {
		return nil, err
	}
	// Non-fatal include notices ride out on the Config because this package
	// cannot print; the CLI boundary emits them. Rendered here because this is
	// where the top-level config's directory is known — every include path in
	// a warning is anchored to it, so a nested tree cannot produce two
	// different files that both print as "../pvmt.toml". Assigned before
	// Validate so a config that then fails validation still carries them for
	// any caller that inspects it.
	cfg.loadWarnings = warn.render(filepath.Dir(abs))
	// SourcePath keeps the caller's spelling (used by export/server to read the
	// raw TOML back); ConfigID and contentHash key on the absolute path and the
	// merged file contents so two spellings of the same file agree.
	cfg.SourcePath = path
	if cfg.ConfigID == "" {
		cfg.ConfigID = hashBytes([]byte(abs))
	}
	// Post-merge: enforce the full invariants the relaxed per-file parse defers
	// (≥1 city across the merged set, unique slugs, per-city shape).
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	// Fold every included file's bytes into the content hash (in declaration
	// order) so editing any included example changes the snapshot hash and
	// triggers recompute. Each blob is length-prefixed before hashing so the
	// partition between files is unambiguous: a plain concatenation would hash
	// two different include layouts of the same total bytes identically (e.g.
	// files ("ab","c") vs ("a","bc")), silently sharing a snapshot. Pre-1.0, so
	// this changes the hash value for every config (including single-file ones);
	// stale snapshots simply recompute on next run.
	cfg.contentHash = hashBlobs(blobs)
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
	// A file that declares [[include]] may legitimately have no [[cities]] of
	// its own — the cities arrive from the merge in Load. Defer the ≥1-city
	// requirement to the post-merge Validate then; still validate every other
	// invariant (grid, forecast, per-city shape) on whatever cities are present.
	//
	// applyForecastPresence must run BEFORE validate: the [[include]]
	// calibration gate asks forecastIsZero whether this file states a
	// top-level [forecast], and an explicit `growth_rate = 0` is only
	// distinguishable from an absent one via the presence bit this sets.
	// Validating first left the bit false, so a transitive parent could
	// smuggle a zero growth rate past the per-file gate.
	applyForecastPresence(&cfg, data)
	if err := cfg.validate(len(cfg.Include) == 0); err != nil {
		return nil, err
	}
	cfg.contentHash = hashBytes(data)
	return &cfg, nil
}

// forecastPresence shadows just the forecast fields whose absence is
// meaningful, using pointers so "absent" and "explicitly zero" are
// distinguishable. It exists because BurntSushi's MetaData cannot address an
// individual [[cities]] element — IsDefined("cities", "forecast",
// "growth_rate") is false even when a city sets it, and Keys() flattens every
// array element into one undifferentiated list — so per-city presence has to
// come from a second decode rather than from decode metadata.
type forecastPresence struct {
	Forecast struct {
		GrowthRate *float64 `toml:"growth_rate"`
	} `toml:"forecast"`
	Cities []struct {
		Forecast *struct {
			GrowthRate *float64 `toml:"growth_rate"`
		} `toml:"forecast"`
	} `toml:"cities"`
}

// applyForecastPresence records which forecast fields the TOML stated
// explicitly. Decode errors are impossible here (the same bytes already decoded
// into a full Config above) and are ignored rather than reported: presence is a
// refinement, and failing the whole load over it would be worse than falling
// back to the zero-as-unset behaviour.
func applyForecastPresence(cfg *Config, data []byte) {
	var p forecastPresence
	if _, err := toml.Decode(string(data), &p); err != nil {
		return
	}
	cfg.Forecast.growthRateSet = p.Forecast.GrowthRate != nil
	for i := range cfg.Cities {
		if i >= len(p.Cities) || cfg.Cities[i].Forecast == nil {
			continue
		}
		pc := p.Cities[i].Forecast
		cfg.Cities[i].Forecast.growthRateSet = pc != nil && pc.GrowthRate != nil
	}
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
	return c.validate(true)
}

// validate is Validate with a switch for the ≥1-city requirement. parseConfig
// passes requireCities=false for an include-bearing file (its cities arrive
// during the Load merge); Load re-runs the full check post-merge.
func (c *Config) validate(requireCities bool) error {
	if requireCities && len(c.Cities) == 0 {
		return errors.Join(ErrInvalidConfig, ErrNoCities)
	}
	// A config that pulls cities in via [[include]] must keep its own top-level
	// calibration empty. The merge flattens each included example's calibration
	// into per-city overrides only for the fields that example set, so a
	// non-empty top-level here would silently re-calibrate every city that
	// inherited a package default — hundreds of forecast numbers changing with no
	// diagnostic. Reject it (documented invariant; no shipped example violates
	// it). Runs both per-file (a bad including file, incl. transitive parents)
	// and post-merge, where Include is still populated and the top-level tables
	// are unchanged, so it stays satisfied.
	//
	// display.min_hex_area is gated for the same reason, and gated *because*
	// hex_edge_m is: the two are coupled (effectiveMinHexArea), so a parent
	// threshold sized for the default 100 m grid can silently blank a metro that
	// flattened a fine hex edge — greater-boston's 60 m Cambridge grid has ~9.4k
	// m² hexes, and filterHexSlivers drops every one of them under a 20k parent
	// threshold with no error from BuildHexGeoJSON. The gate is field-scoped, not
	// table-scoped: display.units carries no per-city layer and stays legal in an
	// including file (examples/all relies on the rest of [display]/[export]).
	if len(c.Include) > 0 && (!forecastIsZero(c.Forecast) || c.Grid.HexEdgeM > 0 || c.Display.MinHexArea > 0) {
		return errors.Join(ErrInvalidConfig, errors.New(
			"a config with [[include]] must keep top-level [grid]/[forecast] and display.min_hex_area empty; "+
				"they would silently re-calibrate included cities (set per-city or per-example instead)"))
	}
	// nonFinite is load-bearing here, not tidiness. resolveHexEdge gates on
	// `> 0`, which is false for NaN (so a NaN edge harmlessly falls back to the
	// default) but TRUE for +Inf — making +Inf the only non-finite edge that can
	// reach geo.HexGrid from the CLI. There it produces a single hex with an
	// all-NaN polygon whose envelope overlaps nothing, so the R-tree search
	// returns no candidates and the city exports with an empty hex layer and no
	// error anywhere. Reject it at load instead.
	if nonFinite(c.Grid.HexEdgeM) || c.Grid.HexEdgeM < 0 {
		return errors.Join(ErrInvalidConfig,
			fmt.Errorf("grid.hex_edge_m %g must be a non-negative finite number", c.Grid.HexEdgeM))
	}
	if nonFinite(c.Display.MinHexArea) || c.Display.MinHexArea < 0 {
		return errors.Join(ErrInvalidConfig,
			fmt.Errorf("display.min_hex_area %g must be a non-negative finite number", c.Display.MinHexArea))
	}
	// export.coordinate_decimals feeds CoordinateDecimals(), which silently
	// falls back to DefaultCoordinateDecimals for any non-positive value — so a
	// negative literal (a typo) loads clean and the intended precision is lost
	// with no diagnostic. Reject it up front. maxCoordinateDecimals caps the
	// upper end: a float64 lon/lat carries ~15 significant decimal digits, so
	// anything beyond that is spurious precision (almost certainly a typo) and
	// bloats the emitted GeoJSON. 0 is allowed (means "use the default").
	const maxCoordinateDecimals = 15
	if c.Export.CoordinateDecimals < 0 || c.Export.CoordinateDecimals > maxCoordinateDecimals {
		return errors.Join(ErrInvalidConfig,
			fmt.Errorf("export.coordinate_decimals %d out of range (0-%d, or 0 for default)",
				c.Export.CoordinateDecimals, maxCoordinateDecimals))
	}
	// export.boundary_simplify_m is deliberately permissive about sign — a
	// negative value is the byte-exact opt-out (see BoundarySimplifyM) — but
	// NaN and ±Inf must be rejected, and not for tidiness: TOML accepts the
	// `nan` and `inf` float literals, `NaN > maxBoundarySimplifyM` is false, so
	// such a value loads clean and reaches the simplifier. simplefeatures'
	// Ramer-Douglas-Peucker breaks its inner loop on `maxDist <= threshold`,
	// which is never true for NaN, so the search interval never collapses and
	// the export HANGS rather than failing. (A negative threshold has the same
	// non-termination property, which is why the simplifier short-circuits the
	// opt-out before calling Simplify rather than passing it through.)
	//
	// The upper bound is a typo guard: past a kilometre the "boundary" is no
	// longer recognizable as the city, and nobody wants that on purpose.
	const maxBoundarySimplifyM = 1000.0
	if nonFinite(c.Export.BoundarySimplifyM) {
		return errors.Join(ErrInvalidConfig,
			fmt.Errorf("export.boundary_simplify_m must be a finite number, got %g",
				c.Export.BoundarySimplifyM))
	}
	if c.Export.BoundarySimplifyM > maxBoundarySimplifyM {
		return errors.Join(ErrInvalidConfig,
			fmt.Errorf("export.boundary_simplify_m %g exceeds the %g m maximum (0 for default, negative to disable)",
				c.Export.BoundarySimplifyM, maxBoundarySimplifyM))
	}
	// Reject an unknown display.units rather than silently rendering imperial:
	// units.ParseSystem maps any unrecognized string (incl. typos like "metirc")
	// to Imperial, so without this a misspelling loads clean and misreports every
	// area. The PVMT_UNITS env path already gates on IsKnown; this closes the
	// config-file gap (solvent-streets-dfm5). Empty is allowed (means "default").
	if c.Display.Units != "" && !units.IsKnown(c.Display.Units) {
		return errors.Join(ErrInvalidConfig,
			fmt.Errorf("display.units %q is not a known unit system (want \"metric\" or \"imperial\")", c.Display.Units))
	}
	if err := c.Forecast.Validate(); err != nil {
		return errors.Join(ErrInvalidConfig, err)
	}
	return c.validateCities()
}

// validateTags rejects blank tag labels on one city. A blank tag is never what
// the author meant, and it groups the city under the exporter's "untagged"
// bucket rather than a named optgroup — a silent mis-grouping. unionTags strips
// empties, but only for cities arriving through [[include]]; a directly
// declared city bypasses it. Split out of validateCities for the same reason
// that function was split out of validate: cognitive complexity.
func validateTags(i int, city CityConfig) error {
	for j, tag := range city.Tags {
		if strings.TrimSpace(tag) == "" {
			return errors.Join(ErrInvalidConfig,
				fmt.Errorf("cities[%d] (%s): tags[%d] is empty; remove it or give it a label", i, city.Name, j))
		}
	}
	return nil
}

// validateCities checks per-city shape invariants and slug uniqueness. Split
// from validate to keep each function's cognitive complexity in bounds.
func (c *Config) validateCities() error {
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
		if err := validateCityFields(i, city); err != nil {
			return err
		}
		seen[slug] = true
	}
	return nil
}

// validateCityFields checks the per-city field-shape invariants (hex edge,
// min hex area, boundary relation, data source, tags, forecast) for one city.
// Split from validateCities, which keeps the name/slug/uniqueness loop, to hold each
// function's cognitive complexity in bounds.
func validateCityFields(i int, city CityConfig) error {
	if nonFinite(city.HexEdgeM) || city.HexEdgeM < 0 {
		return errors.Join(ErrInvalidConfig,
			fmt.Errorf("cities[%d] (%s): hex_edge_m %g must be a non-negative finite number", i, city.Name, city.HexEdgeM))
	}
	// Mirrors the top-level display.min_hex_area check in validate: a negative
	// literal would fall through ResolvedMinHexArea's `> 0` guard to the
	// top-level/default value with no diagnostic, silently discarding the
	// threshold the author meant to set. 0 is allowed (means "inherit").
	if nonFinite(city.MinHexArea) || city.MinHexArea < 0 {
		return errors.Join(ErrInvalidConfig,
			fmt.Errorf("cities[%d] (%s): min_hex_area %g must be a non-negative finite number", i, city.Name, city.MinHexArea))
	}
	if city.BoundaryRelationID < 0 {
		return errors.Join(ErrInvalidConfig,
			fmt.Errorf("cities[%d] (%s): boundary_relation_id %d must be non-negative", i, city.Name, city.BoundaryRelationID))
	}
	// A city with neither Overpass nor an ArcGIS URL has no data source.
	// ingest.AllSources includes OverpassSource only when overpass = true and
	// ArcGISSource only when arcgis_url is set, so this combination resolves to
	// zero sources: the ingest pipeline has nothing to fetch and the city
	// silently produces an empty dataset (zero roads/parking/sidewalks) that
	// renders as a blank heatmap with no error. Reject it at load time — every
	// shipped example sets overpass = true. Note overpass defaults to Go's zero
	// value false when the key is omitted; a city that wants Overpass must set
	// it explicitly. Empty/whitespace arcgis_url counts as unset.
	if !city.Overpass && strings.TrimSpace(city.ArcGISURL) == "" {
		return errors.Join(ErrInvalidConfig,
			fmt.Errorf("cities[%d] (%s): no data source; set overpass = true or provide arcgis_url", i, city.Name))
	}
	if err := validateTags(i, city); err != nil {
		return err
	}
	if city.Forecast != nil {
		if err := city.Forecast.Validate(); err != nil {
			return errors.Join(ErrInvalidConfig,
				fmt.Errorf("cities[%d] (%s): %w", i, city.Name, err))
		}
	}
	return nil
}
