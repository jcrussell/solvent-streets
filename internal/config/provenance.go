package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/jcrussell/solvent-streets/internal/units"
)

// Layered resolution (byob-config.2).
//
// Every multi-layer config field is resolved through a fixed precedence
// chain and carries a Source describing which layer supplied the value:
//
//	flag    (highest, when a CLI flag is available for the field)
//	env     (PVMT_<UPPER_SNAKE> environment variable)
//	city    (per-city [[cities]] override inside the file)
//	file    (top-level pvmt.toml value)
//	default (built-in fallback, lowest)
//
// The SourceKind string values ("flag", "env", "file", "default") are
// part of the public contract — `config show --json` writes them and
// downstream jq/template consumers parse them. `Source.Detail` names
// the specific origin within a kind (`PVMT_UNITS`, `grid.hex_edge_m`,
// `cities[detroit].forecast.years`, `--units`); the format is
// `<kind>[:<detail>]` via Source.String, also stable.
//
// Invalid or out-of-range env values are ignored at parse time and fall
// through to the next layer down so the merged config remains
// well-typed; the warnInvalidEnv middleware at the CLI boundary
// surfaces the rejection to the user.

type SourceKind string

const (
	SourceDefault SourceKind = "default"
	SourceEnv     SourceKind = "env"
	SourceFile    SourceKind = "file"
	SourceFlag    SourceKind = "flag"
)

type Source struct {
	Kind   SourceKind `json:"kind"`
	Detail string     `json:"detail,omitempty"`
}

func (s Source) String() string {
	if s.Detail == "" {
		return string(s.Kind)
	}
	return string(s.Kind) + ":" + s.Detail
}

type ResolvedField struct {
	Key    string `json:"key"`
	Value  any    `json:"value"`
	Source Source `json:"source"`
}

// ExportData satisfies cmdutil.RowExporter for --json output. Source is
// flattened to a map so gojq (which requires JSON-native types) can
// traverse into .source.kind / .source.detail via --jq expressions.
func (r ResolvedField) ExportData(fields []string) map[string]any {
	m := make(map[string]any, len(fields))
	for _, f := range fields {
		switch f {
		case "key":
			m[f] = r.Key
		case "value":
			m[f] = r.Value
		case "source":
			src := map[string]any{"kind": string(r.Source.Kind)}
			if r.Source.Detail != "" {
				src["detail"] = r.Source.Detail
			}
			m[f] = src
		}
	}
	return m
}

// ResolvedFieldNames is the closed vocabulary accepted by --json.
var ResolvedFieldNames = []string{"key", "value", "source"}

// resolveUnits returns the resolved unit system and its source. flagUnits
// is the --units flag value ("" when unset) — passed in rather than read
// from cobra so this package stays framework-free.
func (c *Config) resolveUnits(flagUnits string) (units.System, Source) {
	if flagUnits != "" {
		return units.ParseSystem(flagUnits), Source{Kind: SourceFlag, Detail: "--units"}
	}
	if v, ok := os.LookupEnv("PVMT_UNITS"); ok && v != "" && units.IsKnown(v) {
		return units.ParseSystem(v), Source{Kind: SourceEnv, Detail: "PVMT_UNITS"}
	}
	if c.Display.Units != "" {
		return units.ParseSystem(c.Display.Units), Source{Kind: SourceFile, Detail: "display.units"}
	}
	return units.Imperial, Source{Kind: SourceDefault}
}

// hexEdgeFromEnv reads the PVMT_HEX_EDGE_M override. The bool reports whether
// a valid positive value was present. Shared by resolveHexEdge and
// resolveHexEdgeForCity so the env layer is read identically in both.
func hexEdgeFromEnv() (float64, Source, bool) {
	if v, ok := os.LookupEnv("PVMT_HEX_EDGE_M"); ok && v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f, Source{Kind: SourceEnv, Detail: "PVMT_HEX_EDGE_M"}, true
		}
	}
	return 0, Source{}, false
}

// resolveHexEdge returns the top-level hex edge and its source.
func (c *Config) resolveHexEdge() (float64, Source) {
	if f, src, ok := hexEdgeFromEnv(); ok {
		return f, src
	}
	if c.Grid.HexEdgeM > 0 {
		return c.Grid.HexEdgeM, Source{Kind: SourceFile, Detail: "grid.hex_edge_m"}
	}
	return DefaultHexEdgeM, Source{Kind: SourceDefault}
}

// resolveHexEdgeForCity returns the hex edge for a city and its source.
// Precedence is env > city > file > default, matching the package contract
// and resolveForecast: PVMT_HEX_EDGE_M beats a per-city override, which in
// turn beats the top-level grid value.
func (c *Config) resolveHexEdgeForCity(city *CityConfig) (float64, Source) {
	if f, src, ok := hexEdgeFromEnv(); ok {
		return f, src
	}
	if city != nil && city.HexEdgeM > 0 {
		return city.HexEdgeM, Source{
			Kind:   SourceFile,
			Detail: fmt.Sprintf("cities[%s].hex_edge_m", city.Slug()),
		}
	}
	return c.resolveHexEdge()
}

// forecastProvenance records a source label for each resolved forecast
// field. Every field is populated after resolveForecast returns.
type forecastProvenance struct {
	InitialPCI    Source
	DecayRate     Source
	GrowthRate    Source
	Years         Source
	CostTiers     Source
	CurrentBudget Source
}

// resolveForecast returns the merged forecast config (env > city > file >
// default) with a source for each field. Mirrors ResolvedForecast's
// precedence exactly — ResolvedForecast is now a thin wrapper.
//
// Fields that config can't default (DecayRate, GrowthRate, CostTiers) are
// reported as SourceDefault when no layer supplies them; callers that need
// the forecast-package defaults apply them downstream.
func (c *Config) resolveForecast(city *CityConfig) (ForecastConfig, forecastProvenance) {
	fc := c.Forecast
	prov := fileForecastProv(&c.Forecast)
	applyCityForecastProv(&fc, &prov, city)
	applyEnvForecastProv(&fc, &prov)
	applyDefaultForecastProv(&fc, &prov)
	return fc, prov
}

func fileForecastProv(fc *ForecastConfig) forecastProvenance {
	var p forecastProvenance
	if fc.InitialPCI > 0 && fc.InitialPCI <= 100 {
		p.InitialPCI = Source{Kind: SourceFile, Detail: "forecast.initial_pci"}
	}
	if fc.DecayRate > 0 {
		p.DecayRate = Source{Kind: SourceFile, Detail: "forecast.decay_rate"}
	}
	if fc.GrowthRate != 0 {
		p.GrowthRate = Source{Kind: SourceFile, Detail: "forecast.growth_rate"}
	}
	if fc.Years > 0 {
		p.Years = Source{Kind: SourceFile, Detail: "forecast.years"}
	}
	if len(fc.CostTiers) > 0 {
		p.CostTiers = Source{Kind: SourceFile, Detail: "forecast.cost_tiers"}
	}
	if fc.CurrentBudget > 0 {
		p.CurrentBudget = Source{Kind: SourceFile, Detail: "forecast.current_budget"}
	}
	return p
}

func applyCityForecastProv(fc *ForecastConfig, p *forecastProvenance, city *CityConfig) {
	if city == nil || city.Forecast == nil {
		return
	}
	slug := city.Slug()
	ov := city.Forecast
	if ov.InitialPCI > 0 && ov.InitialPCI <= 100 {
		fc.InitialPCI = ov.InitialPCI
		p.InitialPCI = Source{Kind: SourceFile, Detail: fmt.Sprintf("cities[%s].forecast.initial_pci", slug)}
	}
	if ov.DecayRate > 0 {
		fc.DecayRate = ov.DecayRate
		p.DecayRate = Source{Kind: SourceFile, Detail: fmt.Sprintf("cities[%s].forecast.decay_rate", slug)}
	}
	// Match fileForecastProv's `!= 0` sentinel: a negative per-city growth_rate
	// (shrinking network) is valid per ForecastConfig.Validate and must not be
	// silently dropped. An explicit per-city 0 remains inexpressible with a
	// value-type float (see docs/configuration.md caveat).
	if ov.GrowthRate != 0 {
		fc.GrowthRate = ov.GrowthRate
		p.GrowthRate = Source{Kind: SourceFile, Detail: fmt.Sprintf("cities[%s].forecast.growth_rate", slug)}
	}
	if ov.Years > 0 {
		fc.Years = ov.Years
		p.Years = Source{Kind: SourceFile, Detail: fmt.Sprintf("cities[%s].forecast.years", slug)}
	}
	if len(ov.CostTiers) > 0 {
		fc.CostTiers = ov.CostTiers
		p.CostTiers = Source{Kind: SourceFile, Detail: fmt.Sprintf("cities[%s].forecast.cost_tiers", slug)}
	}
	if ov.CurrentBudget > 0 {
		fc.CurrentBudget = ov.CurrentBudget
		p.CurrentBudget = Source{Kind: SourceFile, Detail: fmt.Sprintf("cities[%s].forecast.current_budget", slug)}
	}
}

func applyEnvForecastProv(fc *ForecastConfig, p *forecastProvenance) {
	if n, ok := parsePositiveIntEnv("PVMT_FORECAST_YEARS"); ok {
		fc.Years = n
		p.Years = Source{Kind: SourceEnv, Detail: "PVMT_FORECAST_YEARS"}
	}
	if f, ok := parsePCIEnv("PVMT_FORECAST_INITIAL_PCI"); ok {
		fc.InitialPCI = f
		p.InitialPCI = Source{Kind: SourceEnv, Detail: "PVMT_FORECAST_INITIAL_PCI"}
	}
}

func applyDefaultForecastProv(fc *ForecastConfig, p *forecastProvenance) {
	if fc.InitialPCI <= 0 || fc.InitialPCI > 100 {
		fc.InitialPCI = DefaultInitialPCI
		p.InitialPCI = Source{Kind: SourceDefault}
	}
	if fc.Years <= 0 {
		fc.Years = DefaultForecastYears
		p.Years = Source{Kind: SourceDefault}
	}
	if (p.DecayRate == Source{}) {
		p.DecayRate = Source{Kind: SourceDefault}
	}
	if (p.GrowthRate == Source{}) {
		p.GrowthRate = Source{Kind: SourceDefault}
	}
	if (p.CostTiers == Source{}) {
		p.CostTiers = Source{Kind: SourceDefault}
	}
	if (p.CurrentBudget == Source{}) {
		p.CurrentBudget = Source{Kind: SourceDefault}
	}
}

// Resolve returns the layered config fields with their resolved values
// and sources. flagUnits is the --units flag ("" when unset).
//
// Scope: only fields with multi-layer resolution (env/flag/city/file/
// default) are emitted — that's the diagnostic surface "--sources" is
// actually for. Fields with single-layer resolution (forecast.decay_rate,
// forecast.growth_rate, forecast.cost_tiers, export.*) are visible in
// the default TOML output and would be misleading here: decay_rate and
// cost_tiers pull their zero-value defaults from the forecast package
// downstream of config, so config can't report the effective value
// without importing forecast, and the existing layering keeps those
// packages decoupled.
//
// Fields are emitted in a stable order: top-level first, then one block
// per city containing only the fields that city explicitly overrides.
func (c *Config) Resolve(flagUnits string) []ResolvedField {
	fields := make([]ResolvedField, 0, 4+3*len(c.Cities))

	unitsVal, unitsSrc := c.resolveUnits(flagUnits)
	fields = append(fields, ResolvedField{Key: "units", Value: unitsVal.String(), Source: unitsSrc})

	hexVal, hexSrc := c.resolveHexEdge()
	fields = append(fields, ResolvedField{Key: "grid.hex_edge_m", Value: hexVal, Source: hexSrc})

	fc, fprov := c.resolveForecast(nil)
	fields = append(fields,
		ResolvedField{Key: "forecast.initial_pci", Value: fc.InitialPCI, Source: fprov.InitialPCI},
		ResolvedField{Key: "forecast.years", Value: fc.Years, Source: fprov.Years},
	)
	// current_budget is emitted only when configured (>0); uncalibrated
	// configs omit it rather than reporting a fabricated $0.
	if fc.CurrentBudget > 0 {
		fields = append(fields, ResolvedField{Key: "forecast.current_budget", Value: fc.CurrentBudget, Source: fprov.CurrentBudget})
	}

	for i := range c.Cities {
		city := &c.Cities[i]
		slug := city.Slug()
		if city.HexEdgeM > 0 {
			fields = append(fields, ResolvedField{
				Key:    fmt.Sprintf("cities[%s].hex_edge_m", slug),
				Value:  city.HexEdgeM,
				Source: Source{Kind: SourceFile, Detail: fmt.Sprintf("cities[%s].hex_edge_m", slug)},
			})
		}
		if city.Forecast == nil {
			continue
		}
		if city.Forecast.InitialPCI > 0 && city.Forecast.InitialPCI <= 100 {
			fields = append(fields, ResolvedField{
				Key:    fmt.Sprintf("cities[%s].forecast.initial_pci", slug),
				Value:  city.Forecast.InitialPCI,
				Source: Source{Kind: SourceFile, Detail: fmt.Sprintf("cities[%s].forecast.initial_pci", slug)},
			})
		}
		if city.Forecast.Years > 0 {
			fields = append(fields, ResolvedField{
				Key:    fmt.Sprintf("cities[%s].forecast.years", slug),
				Value:  city.Forecast.Years,
				Source: Source{Kind: SourceFile, Detail: fmt.Sprintf("cities[%s].forecast.years", slug)},
			})
		}
		if city.Forecast.CurrentBudget > 0 {
			fields = append(fields, ResolvedField{
				Key:    fmt.Sprintf("cities[%s].forecast.current_budget", slug),
				Value:  city.Forecast.CurrentBudget,
				Source: Source{Kind: SourceFile, Detail: fmt.Sprintf("cities[%s].forecast.current_budget", slug)},
			})
		}
	}

	return fields
}
