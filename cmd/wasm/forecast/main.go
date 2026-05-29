//go:build js && wasm

// Command forecast-wasm is a WASM entry point that exposes the Go forecast
// simulation to the browser via syscall/js.
//
// Build: GOOS=js GOARCH=wasm go build -o forecast.wasm ./cmd/wasm/forecast
package main

import (
	"encoding/json"
	"syscall/js"

	"github.com/jcrussell/solvent-streets/internal/forecast"
)

// wasmInput mirrors the JSON structure sent from the browser controls.
type wasmInput struct {
	Area         float64        `json:"area"`
	InitialPCI   float64        `json:"initial_pci"`
	DecayRate    float64        `json:"decay_rate"`
	GrowthRate   float64        `json:"growth_rate"`
	Years        int            `json:"years"`
	CostTiers    []wasmCostTier `json:"cost_tiers"`
	AnnualBudget float64        `json:"annual_budget"`
	Strategy     string         `json:"strategy"`
	Cohorts      []wasmCohort   `json:"cohorts,omitempty"`
}

type wasmCohort struct {
	Classification string  `json:"classification"`
	Area           float64 `json:"area"`
	DecayRate      float64 `json:"decay_rate"`
}

type wasmCostTier struct {
	MinPCI     float64 `json:"min_pci"`
	MaxPCI     float64 `json:"max_pci"`
	CostPerSqM float64 `json:"cost_per_sqm"`
	Label      string  `json:"label"`
}

func simulateForecast(_ js.Value, args []js.Value) any {
	if len(args) < 1 {
		return js.ValueOf(`{"error":"missing input argument"}`)
	}

	raw := args[0].String()
	var input wasmInput
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		errJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
		return js.ValueOf(string(errJSON))
	}

	// Build cost tiers
	var tiers []forecast.CostTier
	for _, t := range input.CostTiers {
		tiers = append(tiers, forecast.CostTier{
			MinPCI:     t.MinPCI,
			MaxPCI:     t.MaxPCI,
			CostPerSqM: t.CostPerSqM,
			Label:      t.Label,
		})
	}

	params := forecast.NewParams(input.GrowthRate, tiers)

	// Parse strategy
	strategy, err := forecast.ParseStrategy(input.Strategy)
	if err != nil {
		strategy = forecast.StrategyDoNothing
	}

	scenario := forecast.Scenario{
		Name:         "custom",
		Label:        "Custom Scenario",
		AnnualBudget: input.AnnualBudget,
		Strategy:     strategy,
	}

	// Build cohorts from input or create a single cohort
	var cohorts []forecast.Cohort
	if len(input.Cohorts) > 0 {
		for _, c := range input.Cohorts {
			cohorts = append(cohorts, forecast.Cohort{
				Classification: c.Classification,
				Area:           c.Area,
				DecayRate:      c.DecayRate,
				InitialPCI:     input.InitialPCI,
			})
		}
	} else {
		decayRate := input.DecayRate
		if decayRate <= 0 {
			decayRate = forecast.DefaultDecayRates["default"]
		}
		cohorts = []forecast.Cohort{{
			Classification: "default",
			Area:           input.Area,
			DecayRate:      decayRate,
			InitialPCI:     input.InitialPCI,
		}}
	}

	// Load-bearing call: shared with the CLI forecast path. Output is
	// locked down by internal/forecast/parity_test.go — any drift here
	// or in Simulate breaks both surfaces at once.
	result := forecast.Simulate(
		scenario,
		cohorts,
		input.Years,
		params,
	)

	out, err := json.Marshal(result)
	if err != nil {
		errJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
		return js.ValueOf(string(errJSON))
	}
	return js.ValueOf(string(out))
}

func main() {
	js.Global().Set("simulateForecast", js.FuncOf(simulateForecast))
	// Keep the Go runtime alive.
	select {}
}
