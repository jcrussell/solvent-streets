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

// Input mirrors the JSON structure sent from the browser controls.
// Its json tags are contractually aligned with internal/export/seeds.go's
// ForecastSeedJSON / CohortSeed; bridge_test.go pins that alignment.
type Input struct {
	Area         float64 `json:"area"`
	InitialPCI   float64 `json:"initial_pci"`
	DecayRate    float64 `json:"decay_rate"`
	GrowthRate   float64 `json:"growth_rate"`
	HorizonYears int     `json:"horizon_years"`
	// CostTiers is the forecast's own tier type: the seed marshals
	// forecast.CostTier, and the payload returns it edited.
	CostTiers    []forecast.CostTier `json:"cost_tiers"`
	AnnualBudget float64             `json:"annual_budget"`
	Strategy     string              `json:"strategy"`
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
	// 0 prices at 1.0 (bare), NOT at the config default: the projector reads
	// an unset overhead as bare (forecast.TieredCostProjector.Overhead). This
	// matters: a partially-rebuilt site pairing a new pvmt.wasm with an old
	// per-city forecast_seed.json sends no cost_overhead at all, which
	// decodes to 0 — and a bare multiply would then price the entire page at
	// $0. Bare figures are off by whatever the configured overhead was
	// (nothing at all under the current 1.0 default); blank ones are wrong by
	// everything. A negative or non-finite one is a caller error and is
	// refused by validateInput rather than read as bare.
	CostOverhead float64  `json:"cost_overhead,omitempty"`
	Cohorts      []Cohort `json:"cohorts,omitempty"`
	// Solvency asks for the solvency metrics of these inputs alongside the
	// scenario: the worst-first hold-steady (break-even) budget, and the
	// insolvency year of the scenario as run. The Financials page anchors its
	// budget slider and recomputes its headline with them, so they follow the
	// live Initial PCI, cost multiplier and tiers. Off by default: a bisection costs ~30 simulations.
	Solvency bool `json:"solvency,omitempty"`
	// Scenarios asks for the reference set (forecast.ReferenceScenarios: the
	// do-nothing baseline and the default funding levels) over these cohorts.
	// The Financials charts draw it when the reader maintains only part of the
	// network, which scenarios.json (every resource) cannot answer.
	// Off by default: it costs four simulations.
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

// Cohort mirrors a single per-classification cohort from the browser payload.
type Cohort struct {
	// ResourceType is the resource the cohort belongs to, as the forecast
	// seed carries it. It sets the cohort's cost scale, so sidewalk cohorts
	// are priced at the sidewalk rate; empty prices at the road tiers.
	ResourceType   string  `json:"resource_type"`
	Classification string  `json:"classification"`
	Area           float64 `json:"area"`
	DecayRate      float64 `json:"decay_rate"`
}

// inputNames is what the browser payload calls the inputs forecast.Inputs
// validates, where the JSON key differs from the forecast package's name.
var inputNames = map[string]string{
	forecast.FieldYears:  "horizon_years",
	forecast.FieldBudget: "annual_budget",
}

// validateInput rejects the untrusted browser payload before it reaches
// forecast.Simulate and the Beta condition spread. The forecast bounds are
// forecast.Inputs.Validate's, the same ones a pvmt.toml is loaded under, so a
// seed an export wrote is never refused here. The network area is the one
// input only this payload carries: a NaN or Inf corrupts every output number
// and breaks the final json.Marshal ("unsupported value: NaN").
func validateInput(in Input, cohorts []forecast.RawCohort) error {
	if !(in.Area >= 0) || math.IsInf(in.Area, 0) {
		return fmt.Errorf("area %g must be a non-negative finite number", in.Area)
	}
	return forecast.Inputs{
		InitialPCI:   in.InitialPCI,
		DecayRate:    in.DecayRate,
		GrowthRate:   in.GrowthRate,
		Years:        float64(in.HorizonYears),
		Budget:       in.AnnualBudget,
		CycleYears:   in.CycleYears,
		CostOverhead: in.CostOverhead,
		CostTiers:    in.CostTiers,
		Cohorts:      cohorts,
	}.Validate(inputNames)
}

// Translate converts a decoded Input into the arguments forecast.Simulate
// expects: the Params, the parsed strategy (whose error is
// surfaced, not swallowed), the "custom" scenario, and the cohorts, falling
// back to one whole-network cohort when the payload lists none.
func Translate(in Input) (forecast.Scenario, []forecast.Cohort, int, *forecast.Params, error) {
	raw := make([]forecast.RawCohort, len(in.Cohorts))
	for i, c := range in.Cohorts {
		raw[i] = forecast.RawCohort{
			Classification: c.Classification,
			Area:           c.Area,
			DecayRate:      c.DecayRate,
			CostScale:      forecast.ResourceCostScale(c.ResourceType),
		}
	}

	if err := validateInput(in, raw); err != nil {
		return forecast.Scenario{}, nil, 0, nil, err
	}

	params := forecast.NewParams(in.GrowthRate, in.CostTiers, in.CycleYears, in.CostOverhead)

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
	if len(raw) == 0 {
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

	// Load-bearing call: shared with the CLI forecast path, so any drift
	// here or in Simulate moves both surfaces at once. Simulate's mechanism
	// is asserted by internal/forecast/simulate_invariants_test.go and its
	// headline output by integration's TestExportForecast_SummaryGolden.
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
