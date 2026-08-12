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
	"fmt"
	"math"

	"github.com/jcrussell/solvent-streets/internal/forecast"
)

// finite reports whether f is a real number (not NaN or ±Inf). The browser
// payload is untrusted: a NaN/Inf propagates through Simulate and then makes
// json.Marshal fail with "unsupported value: NaN", and a NaN InitialPCI slips
// past ApplyConditionSpread's degenerate-Beta guards (all NaN comparisons are
// false) to yield NaN PCIs. Reject non-finite values at this boundary instead.
func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// maxYears bounds Input.Years. forecast.Simulate -> EstimateGrowth does
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
// validateInput rejects the untrusted browser payload before it reaches
// forecast.Simulate and the Beta condition spread. Years is bounded because
// EstimateGrowth does make([]float64, years) (years <= 0 panics, a huge value
// OOMs the WASM runtime). The floats are finite/range-checked because NaN/Inf
// corrupts every output number and breaks the final json.Marshal
// ("unsupported value: NaN"), and an InitialPCI outside [0,100] yields a
// negative Beta and garbage anchors (condition.go). finite guards come first so
// NaN can't slip past a bare `< 0` (all NaN comparisons are false).
func validateInput(in Input) error {
	if in.Years <= 0 || in.Years > maxYears {
		return fmt.Errorf("years must be between 1 and %d, got %d", maxYears, in.Years)
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

	// Spread the single mean PCI into a condition distribution, identically to the
	// server export path (internal/export/forecast.go), so the in-browser custom
	// line agrees with the static lines at equal settings. Derives purely from
	// InitialPCI, so nothing new crosses the JS boundary.
	cohorts = forecast.ApplyConditionSpread(cohorts)

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

	// ApplyConditionSpread (in Translate) expands each cohort into per-band
	// sub-cohorts; collapse the resulting summary rows back to one per
	// classification so the browser's cohort-breakdown table matches the static
	// export table instead of showing K duplicate rows per class.
	result.FinalCohorts = forecast.AggregateCohortSummariesByClass(result.FinalCohorts)

	return json.Marshal(result)
}
