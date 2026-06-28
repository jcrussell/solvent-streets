// Package bridge holds the build-tag-free translation between the browser's
// forecast JSON payload and the shared forecast core (forecast.Simulate).
//
// This logic used to live inside cmd/wasm/forecast/main.go behind
// `//go:build js && wasm`, which made it unreachable by host tests. Extracting
// it here lets bridge_test.go exercise the parse -> translate -> simulate path
// on the CI platform and pin the JSON-key contract shared with
// internal/export/seeds.go (ForecastSeedJSON / CohortSeed). main.go is now a
// thin syscall/js shim that delegates to Run.
package bridge

import (
	"encoding/json"

	"github.com/jcrussell/solvent-streets/internal/forecast"
)

// Input mirrors the JSON structure sent from the browser controls.
// Its json tags are contractually aligned with internal/export/seeds.go's
// ForecastSeedJSON / CohortSeed; bridge_test.go pins that alignment.
type Input struct {
	Area         float64    `json:"area"`
	InitialPCI   float64    `json:"initial_pci"`
	DecayRate    float64    `json:"decay_rate"`
	GrowthRate   float64    `json:"growth_rate"`
	Years        int        `json:"years"`
	CostTiers    []CostTier `json:"cost_tiers"`
	AnnualBudget float64    `json:"annual_budget"`
	Strategy     string     `json:"strategy"`
	// CycleYears is the treatment cycle N (annual need = full-network cost / N).
	// Must match the value the static export lines use (seeded via
	// ForecastSeedJSON.TreatmentCycleYears) or the custom line diverges ~N×.
	// 0 is resolved to forecast.DefaultTreatmentCycleYears in Simulate.
	CycleYears float64  `json:"treatment_cycle_years"`
	Cohorts    []Cohort `json:"cohorts,omitempty"`
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
func Translate(in Input) (forecast.Scenario, []forecast.Cohort, int, *forecast.Params, error) {
	var tiers []forecast.CostTier
	for _, t := range in.CostTiers {
		tiers = append(tiers, forecast.CostTier{
			MinPCI:     t.MinPCI,
			MaxPCI:     t.MaxPCI,
			CostPerSqM: t.CostPerSqM,
			Label:      t.Label,
		})
	}

	params := forecast.NewParams(in.GrowthRate, tiers, in.CycleYears)

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

	var cohorts []forecast.Cohort
	if len(in.Cohorts) > 0 {
		for _, c := range in.Cohorts {
			cohorts = append(cohorts, forecast.Cohort{
				Classification: c.Classification,
				Area:           c.Area,
				DecayRate:      c.DecayRate,
				InitialPCI:     in.InitialPCI,
			})
		}
	} else {
		decayRate := in.DecayRate
		if decayRate <= 0 {
			decayRate = forecast.DefaultDecayRates["default"]
		}
		cohorts = []forecast.Cohort{{
			Classification: "default",
			Area:           in.Area,
			DecayRate:      decayRate,
			InitialPCI:     in.InitialPCI,
		}}
	}

	return scenario, cohorts, in.Years, params, nil
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

	return json.Marshal(result)
}
