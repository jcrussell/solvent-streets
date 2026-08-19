package bridge

import (
	"encoding/json"
	"math"
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
	// Translate now spreads the single InitialPCI into a condition distribution
	// (ApplyConditionSpread), so each input cohort becomes several per-band
	// sub-cohorts. Assert the per-class aggregates round-trip: total area is
	// conserved and the area-weighted mean PCI equals the input InitialPCI (80).
	if len(cohorts) <= 2 {
		t.Fatalf("len(cohorts) = %d, expected spread into sub-cohorts", len(cohorts))
	}
	agg := aggregateByClass(cohorts)
	for _, want := range []struct {
		class     string
		area      float64
		decayRate float64
	}{
		{"primary", 1000, 0.05},
		{"residential", 2000, 0.03},
	} {
		got, ok := agg[want.class]
		if !ok {
			t.Fatalf("class %q missing after spread", want.class)
		}
		if math.Abs(got.area-want.area) > 1e-6 {
			t.Errorf("class %q area = %v, want %v", want.class, got.area, want.area)
		}
		if got.decayRate != want.decayRate {
			t.Errorf("class %q decayRate = %v, want %v", want.class, got.decayRate, want.decayRate)
		}
		if math.Abs(got.meanPCI-80) > 1e-6 {
			t.Errorf("class %q mean PCI = %v, want 80 (preserved from input)", want.class, got.meanPCI)
		}
	}
	if params == nil || params.Cost == nil || len(params.Cost.Tiers) != 1 {
		t.Errorf("params cost tiers not built: %+v", params)
	}
}

// classAgg is a small per-classification rollup used to assert that the condition
// spread preserves area and mean PCI.
type classAgg struct {
	area, meanPCI, decayRate float64
}

func aggregateByClass(cohorts []forecast.Cohort) map[string]classAgg {
	type acc struct{ area, areaPCI, decay float64 }
	tmp := make(map[string]*acc)
	for _, c := range cohorts {
		a, ok := tmp[c.Classification]
		if !ok {
			a = &acc{decay: c.DecayRate}
			tmp[c.Classification] = a
		}
		a.area += c.Area
		a.areaPCI += c.InitialPCI * c.Area
	}
	out := make(map[string]classAgg, len(tmp))
	for k, a := range tmp {
		out[k] = classAgg{area: a.area, meanPCI: a.areaPCI / a.area, decayRate: a.decay}
	}
	return out
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
	// The single default cohort is spread into per-band sub-cohorts; aggregate
	// back and assert the original contract (class/area/mean-PCI/decay).
	if len(cohorts) <= 1 {
		t.Fatalf("len(cohorts) = %d, expected spread into sub-cohorts", len(cohorts))
	}
	agg := aggregateByClass(cohorts)
	c, ok := agg["default"]
	if !ok {
		t.Fatalf("classification %q missing after spread (got %v)", "default", agg)
	}
	if math.Abs(c.area-4242) > 1e-6 {
		t.Errorf("Area = %v, want 4242 (mapped from Input.Area)", c.area)
	}
	if math.Abs(c.meanPCI-70) > 1e-6 {
		t.Errorf("mean PCI = %v, want 70", c.meanPCI)
	}
	if want := forecast.DefaultDecayRates["default"]; c.decayRate != want {
		t.Errorf("DecayRate = %v, want default fallback %v", c.decayRate, want)
	}
}

func TestTranslateNoCohortsExplicitDecay(t *testing.T) {
	// A positive DecayRate must be preserved (no fallback).
	in := Input{Area: 100, DecayRate: 0.07, Years: 1, Strategy: "do-nothing"}
	_, cohorts, _, _, err := Translate(in)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if cohorts[0].DecayRate != 0.07 {
		t.Errorf("DecayRate = %v, want 0.07 (preserved)", cohorts[0].DecayRate)
	}
}

// TestTranslateExplicitCohortsDefaultDecay pins that the multi-cohort branch
// resolves a zero decay rate the same way the single-cohort branch does: a
// rate <= 0 falls back to the default. Previously only the single-cohort branch
// applied this fallback, so a multi-cohort payload reported DecayRate=0.
func TestTranslateExplicitCohortsDefaultDecay(t *testing.T) {
	in := Input{
		InitialPCI: 80,
		Years:      2,
		Strategy:   "do-nothing",
		Cohorts: []Cohort{
			{Classification: "primary", Area: 1000, DecayRate: 0}, // zero → default
			{Classification: "residential", Area: 2000, DecayRate: 0.03},
		},
	}
	_, cohorts, _, _, err := Translate(in)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	agg := aggregateByClass(cohorts)
	if want := forecast.DefaultDecayRates["default"]; agg["primary"].decayRate != want {
		t.Errorf("primary decayRate = %v, want default fallback %v", agg["primary"].decayRate, want)
	}
	if agg["residential"].decayRate != 0.03 {
		t.Errorf("residential decayRate = %v, want 0.03 (preserved)", agg["residential"].decayRate)
	}
}

func TestTranslateInvalidStrategy(t *testing.T) {
	if _, _, _, _, err := Translate(Input{Years: 1, Strategy: "worst-first"}); err != nil {
		t.Fatalf("valid strategy returned error: %v", err)
	}
	_, _, _, _, err := Translate(Input{Years: 1, Strategy: "definitely-not-a-strategy"})
	if err == nil {
		t.Fatal("invalid strategy: expected error, got nil")
	}
}

// TestTranslateYearsBounds asserts that a non-positive or absurdly large Years
// is rejected with an error (never a panic/OOM through EstimateGrowth's
// make([]float64, years)), via both Translate and the Run envelope path.
func TestTranslateYearsBounds(t *testing.T) {
	for _, years := range []int{0, -5, 100000} {
		if _, _, _, _, err := Translate(Input{Years: years, Strategy: "do-nothing"}); err == nil {
			t.Errorf("Translate(Years=%d): expected error, got nil", years)
		}
		raw, _ := json.Marshal(Input{Years: years, Strategy: "do-nothing"})
		if _, err := Run(raw); err == nil {
			t.Errorf("Run(Years=%d): expected error envelope, got nil", years)
		}
	}
	// A sane Years still passes.
	if _, _, _, _, err := Translate(Input{Years: 10, Strategy: "do-nothing"}); err != nil {
		t.Errorf("Translate(Years=10): unexpected error %v", err)
	}
}

// TestTranslateNonFiniteInputs asserts NaN/Inf/out-of-range numeric fields are
// rejected at the boundary (not passed into Simulate where they corrupt every
// output and break the final json.Marshal with "unsupported value: NaN"), via
// both Translate and the Run envelope path.
func TestTranslateNonFiniteInputs(t *testing.T) {
	base := func() Input {
		return Input{
			Area: 750_000, InitialPCI: 75, DecayRate: 0.04, GrowthRate: 0.01,
			Years: 10, AnnualBudget: 1_000_000, Strategy: "do-nothing",
		}
	}
	cases := map[string]func(*Input){
		"nan area":        func(in *Input) { in.Area = math.NaN() },
		"neg area":        func(in *Input) { in.Area = -1 },
		"inf initial_pci": func(in *Input) { in.InitialPCI = math.Inf(1) },
		"neg initial_pci": func(in *Input) { in.InitialPCI = -1 },
		"initial_pci>100": func(in *Input) { in.InitialPCI = 101 },
		"nan decay":       func(in *Input) { in.DecayRate = math.NaN() },
		"inf growth":      func(in *Input) { in.GrowthRate = math.Inf(-1) },
		"nan budget":      func(in *Input) { in.AnnualBudget = math.NaN() },
		"neg budget":      func(in *Input) { in.AnnualBudget = -1 },
		"nan tier cost": func(in *Input) {
			in.CostTiers = []CostTier{{MinPCI: 0, MaxPCI: 100, CostPerSqM: math.NaN(), Label: "x"}}
		},
		"nan cohort area":  func(in *Input) { in.Cohorts = []Cohort{{Classification: "c", Area: math.NaN(), DecayRate: 0.04}} },
		"neg cohort area":  func(in *Input) { in.Cohorts = []Cohort{{Classification: "c", Area: -1, DecayRate: 0.04}} },
		"inf cohort decay": func(in *Input) { in.Cohorts = []Cohort{{Classification: "c", Area: 1000, DecayRate: math.Inf(1)}} },
		"neg cohort decay": func(in *Input) { in.Cohorts = []Cohort{{Classification: "c", Area: 1000, DecayRate: -0.5}} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := base()
			mutate(&in)
			if _, _, _, _, err := Translate(in); err == nil {
				t.Errorf("Translate(%s): expected error, got nil", name)
			}
			raw, _ := json.Marshal(in)
			if _, err := Run(raw); err == nil {
				t.Errorf("Run(%s): expected error envelope, got nil", name)
			}
		})
	}
	// The finite base input still passes.
	if _, _, _, _, err := Translate(base()); err != nil {
		t.Errorf("Translate(base): unexpected error %v", err)
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

	// Run must equal forecast.Simulate over the same translated inputs, with the
	// same post-processing Run applies (FinalCohorts collapsed back by class).
	scenario, cohorts, years, params, err := Translate(in)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	sim := forecast.Simulate(scenario, cohorts, years, params)
	sim.FinalCohorts = forecast.AggregateCohortSummariesByClass(sim.FinalCohorts)
	want, err := json.Marshal(sim)
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
		{"CostOverhead", "CostOverhead", "cost_overhead"},
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

// TestRun_MissingCostOverheadPricesBareNotZero is the stale-seed guard.
//
// Input is decoded from untrusted browser JSON. A partially-rebuilt site pairs
// a new pvmt.wasm with an old per-city forecast_seed.json that has no
// cost_overhead field at all, which decodes to 0.0. A bare multiply would then
// price every figure on the page at $0 — and $0 renders as a plausible-looking
// "fully funded" rather than as an error. Bare figures are wrong by ~1.5x;
// blank ones are wrong by everything.
func TestRun_MissingCostOverheadPricesBareNotZero(t *testing.T) {
	base := `{"area":1000000,"initial_pci":70,"decay_rate":0.04,"years":3,
	          "treatment_cycle_years":12,"annual_budget":0,"strategy":"do-nothing"}`

	out, err := Run([]byte(base))
	if err != nil {
		t.Fatalf("Run with no cost_overhead: %v", err)
	}
	var got struct {
		Years []struct {
			AnnualNeed float64 `json:"annual_need"`
		} `json:"years"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Years) == 0 {
		t.Fatal("no years returned")
	}
	if got.Years[0].AnnualNeed <= 0 {
		t.Fatalf("annual_need = %g with an absent cost_overhead; a stale seed must price bare, not zero",
			got.Years[0].AnnualNeed)
	}

	// And an explicit overhead must actually scale, so the test above is not
	// passing because the field is ignored entirely.
	loaded, err := Run([]byte(base[:len(base)-1] + `,"cost_overhead":2}`))
	if err != nil {
		t.Fatalf("Run with cost_overhead: %v", err)
	}
	var got2 struct {
		Years []struct {
			AnnualNeed float64 `json:"annual_need"`
		} `json:"years"`
	}
	if err := json.Unmarshal(loaded, &got2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if want := got.Years[0].AnnualNeed * 2; math.Abs(got2.Years[0].AnnualNeed-want) > 1e-6*want {
		t.Errorf("annual_need at overhead 2 = %g, want %g (bare %g x 2)",
			got2.Years[0].AnnualNeed, want, got.Years[0].AnnualNeed)
	}
}

// A negative overhead is a caller error, not a stale seed, so it is rejected
// rather than clamped — clamping it to bare would hide the mistake.
func TestRun_NegativeCostOverheadIsRejected(t *testing.T) {
	in := `{"area":1000,"initial_pci":70,"decay_rate":0.04,"years":1,
	        "annual_budget":0,"strategy":"do-nothing","cost_overhead":-1}`
	if _, err := Run([]byte(in)); err == nil {
		t.Error("a negative cost_overhead was accepted; want a validation error")
	}
}
