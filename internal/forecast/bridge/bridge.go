// Package bridge holds the build-tag-free translation between the browser's
// forecast JSON payload and the shared forecast core (forecast.Simulate).
//
// It carries no `//go:build js && wasm` constraint, so bridge_test.go can
// exercise the parse -> translate -> simulate path on the host and pin the
// JSON-key contract shared with internal/export/seeds.go (ForecastSeedJSON /
// CohortSeed). cmd/wasm/pvmt is a thin syscall/js shim that delegates to Run.
package bridge

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/jcrussell/solvent-streets/internal/forecast"
)

// finite reports whether f is a real number (not NaN or ±Inf). The browser
// payload is untrusted: a NaN/Inf propagates through Simulate and then makes
// json.Marshal fail with "unsupported value: NaN", and a NaN InitialPCI slips
// past the condition spread's degenerate-Beta guards (all NaN comparisons are
// false) to yield NaN PCIs. Reject non-finite values at this boundary instead.
func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// maxYears bounds Input.HorizonYears. forecast.Simulate -> EstimateGrowth does
// make([]float64, years), so a huge value OOMs the WASM runtime; the shim's
// contract is "bad input returns a JSON error, never panics".
const maxYears = 200

// Input mirrors the JSON structure sent from the browser controls.
// Its json tags are contractually aligned with internal/export/seeds.go's
// ForecastSeedJSON / CohortSeed; bridge_test.go pins that alignment.
type Input struct {
	Area         float64    `json:"area"`
	InitialPCI   float64    `json:"initial_pci"`
	DecayRate    float64    `json:"decay_rate"`
	GrowthRate   float64    `json:"growth_rate"`
	HorizonYears int        `json:"horizon_years"`
	CostTiers    []CostTier `json:"cost_tiers"`
	AnnualBudget float64    `json:"annual_budget"`
	Strategy     string     `json:"strategy"`
	// CycleYears is the treatment cycle N (annual need = full-network cost / N).
	// Must match the value the static export lines use (seeded via
	// ForecastSeedJSON.TreatmentCycleYears) or the custom line diverges ~N×.
	// 0 is resolved to forecast.DefaultTreatmentCycleYears in Simulate.
	CycleYears float64 `json:"treatment_cycle_years"`
	// CostOverhead is the loaded-cost multiplier (ADA + soft costs +
	// contingency; see config.DefaultCostOverhead). Seeded from
	// forecast_seed.json and adjustable live via the forecast page's overhead
	// slider.
	//
	// 0 is resolved to 1.0 (bare), NOT to the config default, by
	// resolveOverhead below. This matters: a partially-rebuilt site pairing a
	// new pvmt.wasm with an old per-city forecast_seed.json sends no
	// cost_overhead at all, which decodes to 0 — and a bare multiply would then
	// price the entire page at $0. Bare figures are off by whatever the
	// configured overhead was (nothing at all under the current 1.0 default);
	// blank ones are wrong by everything.
	CostOverhead float64  `json:"cost_overhead,omitempty"`
	Cohorts      []Cohort `json:"cohorts,omitempty"`
	// Solvency asks for the solvency metrics of these inputs alongside the
	// scenario: the worst-first hold-steady (break-even) budget, and the
	// insolvency year of the scenario as run. The Financials page anchors its
	// budget slider and recomputes its headline with them, so they follow the
	// live Initial PCI, cost multiplier and tiers (solvent-streets-i68t,
	// solvent-streets-a6ci). Off by default: a bisection costs ~30 simulations.
	Solvency bool `json:"solvency,omitempty"`
	// Scenarios asks for the reference set (forecast.ReferenceScenarios: the
	// do-nothing baseline and the default funding levels) over these cohorts.
	// The Financials charts draw it when the reader maintains only part of the
	// network, which scenarios.json (every resource) cannot answer
	// (solvent-streets-ma98). Off by default: it costs four simulations.
	Scenarios bool `json:"scenarios,omitempty"`
}

// Output is a ScenarioResult plus, when Input.Solvency is set, its solvency
// metrics. The embedded result keeps the JSON shape of a plain run.
type Output struct {
	forecast.ScenarioResult
	BreakEvenBudget *float64 `json:"break_even_budget,omitempty"`
	InsolvencyYear  *int     `json:"insolvency_year,omitempty"`
	// Scenarios is the reference set when Input.Scenarios is set.
	Scenarios []forecast.ScenarioResult `json:"scenarios,omitempty"`
}

// resolveOverhead clamps a missing or nonsensical multiplier to 1.0 (bare).
// Input is decoded from untrusted browser JSON, so this is a boundary check,
// not a defaulting convenience.
func resolveOverhead(v float64) float64 {
	if !finite(v) || v <= 0 {
		return 1
	}
	return v
}

// Cohort mirrors a single per-classification cohort from the browser payload.
type Cohort struct {
	Classification string  `json:"classification"`
	Area           float64 `json:"area"`
	DecayRate      float64 `json:"decay_rate"`
}

// CostTier mirrors a single cost tier from the browser payload.
type CostTier struct {
	MinPCI     float64 `json:"min_pci"`
	MaxPCI     float64 `json:"max_pci"`
	CostPerSqM float64 `json:"cost_per_sqm"`
	Label      string  `json:"label"`
}

// Translate converts a decoded Input into the arguments forecast.Simulate
// expects. It reproduces the original cmd/wasm/forecast/main.go logic exactly:
// cost-tier mapping, NewParams construction, ParseStrategy (whose error is
// surfaced, not silently swallowed), the "custom" scenario, and the cohort
// fallback with decay-rate default. This is a faithful extraction, not a
// redesign.
// validateInput rejects the untrusted browser payload before it reaches
// forecast.Simulate and the Beta condition spread. Years is bounded because
// EstimateGrowth does make([]float64, years) (years <= 0 panics, a huge value
// OOMs the WASM runtime). The floats are finite/range-checked because NaN/Inf
// corrupts every output number and breaks the final json.Marshal
// ("unsupported value: NaN"), and an InitialPCI outside [0,100] yields a
// negative Beta and garbage anchors (condition.go). finite guards come first so
// NaN can't slip past a bare `< 0` (all NaN comparisons are false).
func validateInput(in Input) error {
	if in.HorizonYears <= 0 || in.HorizonYears > maxYears {
		return fmt.Errorf("horizon_years must be between 1 and %d, got %d", maxYears, in.HorizonYears)
	}
	if !finite(in.Area) || in.Area < 0 {
		return fmt.Errorf("area must be finite and >= 0, got %g", in.Area)
	}
	if !finite(in.InitialPCI) || in.InitialPCI < 0 || in.InitialPCI > 100 {
		return fmt.Errorf("initial_pci must be finite in [0,100], got %g", in.InitialPCI)
	}
	if !finite(in.DecayRate) {
		return fmt.Errorf("decay_rate must be finite, got %g", in.DecayRate)
	}
	if !finite(in.GrowthRate) {
		return fmt.Errorf("growth_rate must be finite, got %g", in.GrowthRate)
	}
	if !finite(in.AnnualBudget) || in.AnnualBudget < 0 {
		return fmt.Errorf("annual_budget must be finite and >= 0, got %g", in.AnnualBudget)
	}
	// Absent (0) is tolerated and clamped to bare by resolveOverhead — see the
	// stale-seed note on Input.CostOverhead. A NEGATIVE one is rejected rather
	// than clamped: it can only be a caller error, and silently treating it as
	// bare would hide that.
	if !finite(in.CostOverhead) || in.CostOverhead < 0 {
		return fmt.Errorf("cost_overhead must be finite and >= 0, got %g", in.CostOverhead)
	}
	return validateInputCollections(in)
}

// validateInputCollections checks the per-tier and per-cohort float fields.
func validateInputCollections(in Input) error {
	for i, t := range in.CostTiers {
		if !finite(t.CostPerSqM) || t.CostPerSqM < 0 {
			return fmt.Errorf("cost_tier %d (%q) has invalid cost_per_sqm %g (want finite >= 0)", i, t.Label, t.CostPerSqM)
		}
	}
	for i, c := range in.Cohorts {
		if !finite(c.Area) || c.Area < 0 {
			return fmt.Errorf("cohort %d (%q) has invalid area %g (want finite >= 0)", i, c.Classification, c.Area)
		}
		if !finite(c.DecayRate) || c.DecayRate < 0 {
			return fmt.Errorf("cohort %d (%q) has invalid decay_rate %g (want finite >= 0)", i, c.Classification, c.DecayRate)
		}
	}
	return nil
}

func Translate(in Input) (forecast.Scenario, []forecast.Cohort, int, *forecast.Params, error) {
	if err := validateInput(in); err != nil {
		return forecast.Scenario{}, nil, 0, nil, err
	}

	var tiers []forecast.CostTier
	for _, t := range in.CostTiers {
		tiers = append(tiers, forecast.CostTier{
			MinPCI:     t.MinPCI,
			MaxPCI:     t.MaxPCI,
			CostPerSqM: t.CostPerSqM,
			Label:      t.Label,
		})
	}

	params := forecast.NewParams(in.GrowthRate, tiers, in.CycleYears, resolveOverhead(in.CostOverhead))

	strategy, err := forecast.ParseStrategy(in.Strategy)
	if err != nil {
		return forecast.Scenario{}, nil, 0, nil, err
	}

	scenario := forecast.Scenario{
		Name:         "custom",
		Label:        "Custom Scenario",
		AnnualBudget: in.AnnualBudget,
		Strategy:     strategy,
	}

	// SpreadCohorts is the constructor every forecast surface shares: it applies
	// the single mean PCI, resolves a missing/zero decay rate to the default (so a
	// cohort's reported DecayRate is the rate actually simulated), and spreads the
	// mean into the condition distribution, so the in-browser custom line agrees
	// with the server export's static lines at equal settings.
	var raw []forecast.RawCohort
	if len(in.Cohorts) > 0 {
		raw = make([]forecast.RawCohort, len(in.Cohorts))
		for i, c := range in.Cohorts {
			raw[i] = forecast.RawCohort{
				Classification: c.Classification,
				Area:           c.Area,
				DecayRate:      c.DecayRate,
			}
		}
	} else {
		raw = []forecast.RawCohort{{
			Classification: "default",
			Area:           in.Area,
			DecayRate:      in.DecayRate,
		}}
	}
	cohorts := forecast.SpreadCohorts(raw, in.InitialPCI)

	return scenario, cohorts, in.HorizonYears, params, nil
}

// Run decodes a raw browser payload, translates it, runs the forecast, and
// marshals the result. main.go's syscall/js shim wraps the returned error as
// the same `{"error": ...}` JSON it always has, so the output for any input is
// byte-identical to the pre-extraction code.
func Run(raw []byte) ([]byte, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}

	scenario, cohorts, years, params, err := Translate(in)
	if err != nil {
		return nil, err
	}

	// Load-bearing call: shared with the CLI forecast path. Output is
	// locked down by internal/forecast/parity_test.go — any drift here
	// or in Simulate breaks both surfaces at once.
	result := forecast.Simulate(scenario, cohorts, years, params)

	// Collapse the per-band summary rows to one per class for the browser's
	// cohort-breakdown table; the headline math reads result.Years, unaffected.
	out := Output{ScenarioResult: forecast.CollapseBands(result)}
	if in.Solvency {
		be := forecast.BreakEvenBudget(cohorts, years, params, forecast.StrategyWorstFirst)
		out.BreakEvenBudget = &be
		if yr, ok := forecast.InsolvencyYear(result, params.CycleYears); ok {
			out.InsolvencyYear = &yr
		}
	}
	if in.Scenarios {
		// SpreadCohorts (in Translate) expands each cohort into per-band
		// sub-cohorts; collapse them so the browser's cohort-breakdown table
		// matches the static export table.
		out.Scenarios = forecast.CollapseAllBands(forecast.ReferenceScenarios(cohorts, years, params))
	}
	return json.Marshal(out)
}
