package bridge

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/internal/forecast"
)

func TestTranslateExplicitCohorts(t *testing.T) {
	in := Input{
		InitialPCI:   80,
		GrowthRate:   0.01,
		Years:        5,
		AnnualBudget: 500_000,
		Strategy:     "worst-first",
		CostTiers: []CostTier{
			{MinPCI: 0, MaxPCI: 50, CostPerSqM: 40, Label: "recon"},
		},
		Cohorts: []Cohort{
			{Classification: "primary", Area: 1000, DecayRate: 0.05},
			{Classification: "residential", Area: 2000, DecayRate: 0.03},
		},
	}

	scenario, cohorts, years, params, err := Translate(in)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if years != 5 {
		t.Errorf("years = %d, want 5", years)
	}
	if scenario.Name != "custom" || scenario.Label != "Custom Scenario" {
		t.Errorf("scenario name/label = %q/%q", scenario.Name, scenario.Label)
	}
	if scenario.AnnualBudget != 500_000 {
		t.Errorf("AnnualBudget = %v", scenario.AnnualBudget)
	}
	if scenario.Strategy != forecast.StrategyWorstFirst {
		t.Errorf("Strategy = %v, want WorstFirst", scenario.Strategy)
	}
	if len(cohorts) != 2 {
		t.Fatalf("len(cohorts) = %d, want 2", len(cohorts))
	}
	for i, c := range cohorts {
		if c.InitialPCI != 80 {
			t.Errorf("cohort[%d].InitialPCI = %v, want 80 (propagated from input)", i, c.InitialPCI)
		}
	}
	if cohorts[0].Classification != "primary" || cohorts[0].Area != 1000 || cohorts[0].DecayRate != 0.05 {
		t.Errorf("cohort[0] = %+v", cohorts[0])
	}
	if params == nil || params.Cost == nil || len(params.Cost.Tiers) != 1 {
		t.Errorf("params cost tiers not built: %+v", params)
	}
}

func TestTranslateNoCohortsDefaultFallback(t *testing.T) {
	// DecayRate <= 0 must fall back to the default decay rate.
	in := Input{
		Area:       4242,
		InitialPCI: 70,
		DecayRate:  0,
		Years:      3,
		Strategy:   "do-nothing",
	}

	scenario, cohorts, _, _, err := Translate(in)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if scenario.Strategy != forecast.StrategyDoNothing {
		t.Errorf("Strategy = %v, want DoNothing", scenario.Strategy)
	}
	if len(cohorts) != 1 {
		t.Fatalf("len(cohorts) = %d, want 1 default cohort", len(cohorts))
	}
	c := cohorts[0]
	if c.Classification != "default" {
		t.Errorf("classification = %q, want default", c.Classification)
	}
	if c.Area != 4242 {
		t.Errorf("Area = %v, want 4242 (mapped from Input.Area)", c.Area)
	}
	if c.InitialPCI != 70 {
		t.Errorf("InitialPCI = %v, want 70", c.InitialPCI)
	}
	if want := forecast.DefaultDecayRates["default"]; c.DecayRate != want {
		t.Errorf("DecayRate = %v, want default fallback %v", c.DecayRate, want)
	}
}

func TestTranslateNoCohortsExplicitDecay(t *testing.T) {
	// A positive DecayRate must be preserved (no fallback).
	in := Input{Area: 100, DecayRate: 0.07, Strategy: "do-nothing"}
	_, cohorts, _, _, err := Translate(in)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if cohorts[0].DecayRate != 0.07 {
		t.Errorf("DecayRate = %v, want 0.07 (preserved)", cohorts[0].DecayRate)
	}
}

func TestTranslateInvalidStrategy(t *testing.T) {
	if _, _, _, _, err := Translate(Input{Strategy: "worst-first"}); err != nil {
		t.Fatalf("valid strategy returned error: %v", err)
	}
	_, _, _, _, err := Translate(Input{Strategy: "definitely-not-a-strategy"})
	if err == nil {
		t.Fatal("invalid strategy: expected error, got nil")
	}
}

func TestRunEndToEnd(t *testing.T) {
	in := Input{
		Area:         750_000,
		InitialPCI:   75,
		DecayRate:    0.04,
		GrowthRate:   0.01,
		Years:        10,
		AnnualBudget: 1_000_000,
		Strategy:     "worst-first",
		CostTiers: []CostTier{
			{MinPCI: 0, MaxPCI: 40, CostPerSqM: 50, Label: "reconstruction"},
			{MinPCI: 40, MaxPCI: 70, CostPerSqM: 15, Label: "rehabilitation"},
			{MinPCI: 70, MaxPCI: 100, CostPerSqM: 2, Label: "preventive"},
		},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	out, err := Run(raw)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("Run returned empty output")
	}

	// Run must equal forecast.Simulate over the same translated inputs.
	scenario, cohorts, years, params, err := Translate(in)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	want, err := json.Marshal(forecast.Simulate(scenario, cohorts, years, params))
	if err != nil {
		t.Fatalf("marshal simulate: %v", err)
	}
	if string(out) != string(want) {
		t.Errorf("Run output diverged from forecast.Simulate over the same inputs")
	}

	// Sanity: result decodes and carries the expected number of years.
	var result forecast.ScenarioResult
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Years) != 10 {
		t.Errorf("result has %d years, want 10", len(result.Years))
	}
}

func TestRunMalformedJSON(t *testing.T) {
	if _, err := Run([]byte("{not valid json")); err == nil {
		t.Fatal("malformed JSON: expected error, got nil")
	}
}

func TestRunInvalidStrategyError(t *testing.T) {
	raw, _ := json.Marshal(Input{Strategy: "nope"})
	if _, err := Run(raw); err == nil {
		t.Fatal("invalid strategy via Run: expected error, got nil")
	}
}

// jsonTags maps Go struct field names to their json tag name (sans options).
func jsonTags(typ reflect.Type) map[string]string {
	out := make(map[string]string)
	for f := range typ.Fields() {
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		// Strip ",omitempty" etc.
		name, _, _ := strings.Cut(tag, ",")
		out[f.Name] = name
	}
	return out
}

// TestJSONKeyContract pins the three-way JSON-key contract. The browser
// (templates/index.html.tmpl) builds a payload from the seed JSON produced by
// internal/export.ForecastSeedJSON / CohortSeed, and bridge.Input / Cohort
// decode it. If a json tag is renamed on either the seed side or the bridge
// side, the wasm side silently sees a missing key and substitutes a default
// (budget 0, default decay) — wrong numbers, no error. This test fails on such
// a rename so the contract can't drift unnoticed.
//
// Scope: only the genuinely seed-supplied keys are asserted. bridge-only keys
// (area, growth_rate, years, annual_budget, strategy) are template-supplied,
// not seed-supplied, so they have no ForecastSeedJSON counterpart and are
// intentionally excluded.
func TestJSONKeyContract(t *testing.T) {
	seedTags := jsonTags(reflect.TypeFor[export.ForecastSeedJSON]())
	cohortSeedTags := jsonTags(reflect.TypeFor[export.CohortSeed]())
	inputTags := jsonTags(reflect.TypeFor[Input]())
	bridgeCohortTags := jsonTags(reflect.TypeFor[Cohort]())

	// Shared keys between bridge.Input and export.ForecastSeedJSON, expressed
	// as (bridge field name, seed field name). The values must be the SAME
	// json tag on both sides.
	inputShared := []struct{ bridgeField, seedField, want string }{
		{"InitialPCI", "InitialPCI", "initial_pci"},
		{"DecayRate", "DecayRate", "decay_rate"},
		{"GrowthRate", "GrowthRate", "growth_rate"},
		{"Years", "Years", "years"},
		{"CostTiers", "CostTiers", "cost_tiers"},
		{"Cohorts", "Cohorts", "cohorts"},
	}
	for _, s := range inputShared {
		if got := inputTags[s.bridgeField]; got != s.want {
			t.Errorf("bridge.Input.%s json tag = %q, want %q", s.bridgeField, got, s.want)
		}
		if got := seedTags[s.seedField]; got != s.want {
			t.Errorf("export.ForecastSeedJSON.%s json tag = %q, want %q (renamed?)", s.seedField, got, s.want)
		}
	}

	// Shared keys between bridge.Cohort and export.CohortSeed.
	cohortShared := []struct{ field, want string }{
		{"Classification", "classification"},
		{"Area", "area"},
		{"DecayRate", "decay_rate"},
	}
	for _, s := range cohortShared {
		if got := bridgeCohortTags[s.field]; got != s.want {
			t.Errorf("bridge.Cohort.%s json tag = %q, want %q", s.field, got, s.want)
		}
		if got := cohortSeedTags[s.field]; got != s.want {
			t.Errorf("export.CohortSeed.%s json tag = %q, want %q (renamed?)", s.field, got, s.want)
		}
	}
}
