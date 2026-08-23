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
// a valid positive FINITE value was present. Shared by resolveHexEdge and
// resolveHexEdgeForCity so the env layer is read identically in both.
//
// nonFinite is load-bearing, not tidiness. strconv.ParseFloat accepts "inf" and
// "Infinity" with a nil error and +Inf > 0 is true, so PVMT_HEX_EDGE_M=inf was
// accepted here and went straight to geo.HexGrid — whose guard then returns nil
// and the city exports with an empty hex layer, no error, exit 0. Config's
// load-time gate on grid.hex_edge_m does not cover this: the env layer wins over
// the file value and never passes through Validate.
func hexEdgeFromEnv() (float64, Source, bool) {
	if v, ok := os.LookupEnv("PVMT_HEX_EDGE_M"); ok && v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && !nonFinite(f) && f > 0 {
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

// resolveMinHexArea returns the top-level heatmap sliver threshold and its
// source. There is no PVMT_ env override for display.min_hex_area, so the
// chain is file > default — one layer shorter than resolveHexEdge's.
func (c *Config) resolveMinHexArea() (float64, Source) {
	if c.Display.MinHexArea > 0 {
		return c.Display.MinHexArea, Source{Kind: SourceFile, Detail: "display.min_hex_area"}
	}
	return DefaultMinHexArea, Source{Kind: SourceDefault}
}

// resolveMinHexAreaForCity returns the sliver threshold for a city and its
// source (city > file > default), mirroring resolveHexEdgeForCity — the two
// fields are coupled, so they resolve through the same shape.
func (c *Config) resolveMinHexAreaForCity(city *CityConfig) (float64, Source) {
	if city != nil && city.MinHexArea > 0 {
		return city.MinHexArea, Source{
			Kind:   SourceFile,
			Detail: fmt.Sprintf("cities[%s].min_hex_area", city.Slug()),
		}
	}
	return c.resolveMinHexArea()
}

// forecastProvenance records a source label for each resolved forecast
// field. Every field is populated after resolveForecast returns.
type forecastProvenance struct {
	InitialPCI          Source
	DecayRate           Source
	GrowthRate          Source
	Years               Source
	CostTiers           Source
	CurrentBudget       Source
	TreatmentCycleYears Source
	CostOverhead        Source
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
	// GrowthRate has no "unset" sentinel (0 is a legitimate "no growth"), so an
	// explicit `growth_rate = 0` is labeled SourceDefault here rather than
	// SourceFile (solvent-streets-22kp). Deliberately left alone: no production
	// code reads forecastProvenance.GrowthRate. It is set here, in
	// applyCityForecastProv, and in applyDefaultForecastProv, and consumed only
	// by provenance_test.go's "negative per-city growth_rate is not dropped"
	// case, which asserts the city-layer label — so a future fix must update
	// that test, but changes nothing a user sees: Config.Resolve does not emit
	// forecast.growth_rate at any layer, so `config show --sources` never prints
	// this label.
	//
	// The same `!= 0` sentinel in applyCityForecastProv was NOT inert — there it
	// dropped the value as well as the label, so a city could not opt out of a
	// positive top-level growth rate. That was solvent-streets-r312, now fixed
	// with the ForecastConfig.growthRateSet presence bit.
	if fc.GrowthRate != 0 || fc.growthRateSet {
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
	if fc.TreatmentCycleYears > 0 {
		p.TreatmentCycleYears = Source{Kind: SourceFile, Detail: "forecast.treatment_cycle_years"}
	}
	if fc.CostOverhead > 0 {
		p.CostOverhead = Source{Kind: SourceFile, Detail: "forecast.cost_overhead"}
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
	// growth_rate is the one field here where 0 is a value, not a sentinel: a
	// city that does not grow is a real thing to say, and saying it must
	// override a positive top-level rate. So this consults the presence bit
	// rather than testing != 0. A negative rate (shrinking network) is likewise
	// valid per ForecastConfig.Validate and passes on its own.
	if ov.GrowthRate != 0 || ov.growthRateSet {
		fc.GrowthRate = ov.GrowthRate
		fc.growthRateSet = true
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
	// A positive test is the correct presence check here, unlike growth_rate
	// above: cost_overhead has no meaningful 0 (Validate rejects it), so 0 is
	// unambiguously "unset". A city that wants bare pricing writes 1.0.
	if ov.CostOverhead > 0 {
		fc.CostOverhead = ov.CostOverhead
		p.CostOverhead = Source{Kind: SourceFile, Detail: fmt.Sprintf("cities[%s].forecast.cost_overhead", slug)}
	}
	if ov.TreatmentCycleYears > 0 {
		fc.TreatmentCycleYears = ov.TreatmentCycleYears
		p.TreatmentCycleYears = Source{Kind: SourceFile, Detail: fmt.Sprintf("cities[%s].forecast.treatment_cycle_years", slug)}
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
	// Resolved to forecast.DefaultTreatmentCycleYears downstream (in the forecast
	// core, like DecayRate), so config reports SourceDefault when no layer set it.
	if (p.TreatmentCycleYears == Source{}) {
		p.TreatmentCycleYears = Source{Kind: SourceDefault}
	}
	// Unlike TreatmentCycleYears, the VALUE is resolved here and not downstream:
	// the forecast core reads a zero overhead as bare (1.0) on purpose, so a
	// default applied there would silently load every directly-constructed
	// projector. See ForecastConfig.ResolvedCostOverhead.
	if fc.CostOverhead <= 0 {
		fc.CostOverhead = DefaultCostOverhead
		p.CostOverhead = Source{Kind: SourceDefault}
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
	fields := make([]ResolvedField, 0, 6+4*len(c.Cities))

	unitsVal, unitsSrc := c.resolveUnits(flagUnits)
	fields = append(fields, ResolvedField{Key: "units", Value: unitsVal.String(), Source: unitsSrc})

	hexVal, hexSrc := c.resolveHexEdge()
	fields = append(fields, ResolvedField{Key: "grid.hex_edge_m", Value: hexVal, Source: hexSrc})

	areaVal, areaSrc := c.resolveMinHexArea()
	fields = append(fields, ResolvedField{Key: "display.min_hex_area", Value: areaVal, Source: areaSrc})

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
	// Emitted only when configured (>0); when unset the effective value is the
	// forecast-package default, resolved downstream like decay_rate.
	if fc.TreatmentCycleYears > 0 {
		fields = append(fields, ResolvedField{Key: "forecast.treatment_cycle_years", Value: fc.TreatmentCycleYears, Source: fprov.TreatmentCycleYears})
	}
	// Emitted unconditionally, unlike treatment_cycle_years: applyDefaultForecastProv
	// resolves the VALUE here rather than downstream, so fc.CostOverhead is
	// always a concrete positive multiplier by this point. It is a multi-layer
	// field (file -> per-city -> default) exactly like initial_pci, so leaving it
	// out made `config show --sources` unable to report which layer set the knob
	// every priced dollar passes through.
	fields = append(fields, ResolvedField{Key: "forecast.cost_overhead", Value: fc.CostOverhead, Source: fprov.CostOverhead})

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
		if city.MinHexArea > 0 {
			fields = append(fields, ResolvedField{
				Key:    fmt.Sprintf("cities[%s].min_hex_area", slug),
				Value:  city.MinHexArea,
				Source: Source{Kind: SourceFile, Detail: fmt.Sprintf("cities[%s].min_hex_area", slug)},
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
		if city.Forecast.TreatmentCycleYears > 0 {
			fields = append(fields, ResolvedField{
				Key:    fmt.Sprintf("cities[%s].forecast.treatment_cycle_years", slug),
				Value:  city.Forecast.TreatmentCycleYears,
				Source: Source{Kind: SourceFile, Detail: fmt.Sprintf("cities[%s].forecast.treatment_cycle_years", slug)},
			})
		}
		// Without this the top-level forecast.cost_overhead line is worse than
		// the omission it replaced: a city overriding the multiplier to 1.8
		// would print `forecast.cost_overhead = 1 (default)` and nothing else,
		// reading as "every city prices bare". A positive test is the right
		// presence check (Validate rejects 0), matching applyCityForecastProv.
		if city.Forecast.CostOverhead > 0 {
			fields = append(fields, ResolvedField{
				Key:    fmt.Sprintf("cities[%s].forecast.cost_overhead", slug),
				Value:  city.Forecast.CostOverhead,
				Source: Source{Kind: SourceFile, Detail: fmt.Sprintf("cities[%s].forecast.cost_overhead", slug)},
			})
		}
	}

	return fields
}
