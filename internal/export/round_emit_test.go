package export

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/forecast"
)

func emitTestForecasts() []ForecastExport {
	gap := 1.23456789012345
	bbox := forecast.ScenarioResult{
		Years: []forecast.ScenarioYear{{Year: 1, PCI: 61.234567890123, Area: 704.5835576057434, AnnualNeed: 1234567.8901234567}},
	}
	return []ForecastExport{{
		ResourceType: "roads",
		Baseline: forecast.ScenarioResult{
			Years: []forecast.ScenarioYear{
				{Year: 1, PCI: 61.234567890123, Area: 704.5835576057434, AnnualNeed: 1234567.8901234567, AnnualSpend: 987654.3210987654, DeferredBacklog: 246913.57802469135},
			},
			FinalCohorts: []forecast.CohortSummary{
				{Classification: "residential", EndPCI: 42.987654321, Area: 704.5835576057434, DecayRate: 0.04523456789, TotalSpend: 1234567.8901234567, TotalDeficit: 246913.57802469135},
			},
		},
		BboxBaseline:    &bbox,
		Scenarios:       []forecast.ScenarioResult{bbox},
		BreakEvenBudget: 18234567.891234567,
		CurrentBudget:   15000000.000000002,
		FundingGap:      &gap,
	}}
}

// TestRoundForecastsForEmission_DoesNotMutateInput is the load-bearing property.
// The server memoizes one []ForecastExport behind buildForecasts and hands the
// SAME slice to both serveForecastJSON and serveHexCostSummary, and the static
// export derives BuildHexCostSummary from the same value it writes. Rounding in
// place would corrupt the hex cost summary's inputs and mutate shared cache
// state under concurrent requests.
func TestRoundForecastsForEmission_DoesNotMutateInput(t *testing.T) {
	in := emitTestForecasts()
	before, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got := RoundForecastsForEmission(in)

	after, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("input was mutated by rounding\nbefore: %s\nafter:  %s", before, after)
	}
	if got[0].Baseline.Years[0].AnnualNeed == in[0].Baseline.Years[0].AnnualNeed {
		t.Error("output is not rounded; the test would pass vacuously")
	}
	// The copy must be deep: a shared backing array would let a later write to
	// the rounded slice reach the cached one.
	if &got[0].Baseline.Years[0] == &in[0].Baseline.Years[0] {
		t.Error("Years slice is shared with the input, not copied")
	}
	if got[0].FundingGap == in[0].FundingGap {
		t.Error("FundingGap pointer is shared with the input, not copied")
	}
}

// TestRoundForecastsForEmission_RoundsToDisplayPrecision pins that every rounded
// field lands where the client already renders it, so no published figure moves.
func TestRoundForecastsForEmission_RoundsToDisplayPrecision(t *testing.T) {
	got := RoundForecastsForEmission(emitTestForecasts())[0]

	y := got.Baseline.Years[0]
	checks := []struct {
		name string
		got  float64
		want float64
	}{
		{"years.pci", y.PCI, 61.23},
		{"years.area", y.Area, 705},
		{"years.annual_need", y.AnnualNeed, 1234568},
		{"years.annual_spend", y.AnnualSpend, 987654},
		{"years.deferred_backlog", y.DeferredBacklog, 246914},
		{"break_even_budget", got.BreakEvenBudget, 18234568},
		{"current_budget", got.CurrentBudget, 15000000},
		{"funding_gap", *got.FundingGap, 1.234568},
	}
	c := got.Baseline.FinalCohorts[0]
	checks = append(checks,
		struct {
			name string
			got  float64
			want float64
		}{"final_cohorts.end_pci", c.EndPCI, 42.99},
		struct {
			name string
			got  float64
			want float64
		}{"final_cohorts.decay_rate", c.DecayRate, 0.045235},
		struct {
			name string
			got  float64
			want float64
		}{"final_cohorts.total_spend", c.TotalSpend, 1234568},
	)
	for _, tc := range checks {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}

	// BboxBaseline and Scenarios must be rounded too — they are the same shape
	// and the same size cost, and an unrounded one would look identical on the
	// page while shipping the full float.
	if got.BboxBaseline.Years[0].AnnualNeed != 1234568 {
		t.Errorf("bbox_baseline not rounded: %v", got.BboxBaseline.Years[0].AnnualNeed)
	}
	if got.Scenarios[0].Years[0].AnnualNeed != 1234568 {
		t.Errorf("scenarios not rounded: %v", got.Scenarios[0].Years[0].AnnualNeed)
	}
}

// TestRoundForEmission_ShrinksSerializedBytes is the point of the exercise: the
// emitted JSON must actually get smaller. A rounding that produced the same
// bytes would pass every correctness check above and buy nothing.
func TestRoundForEmission_ShrinksSerializedBytes(t *testing.T) {
	in := emitTestForecasts()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rounded, err := json.Marshal(RoundForecastsForEmission(in))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(rounded) >= len(raw) {
		t.Errorf("rounded payload is %d bytes, unrounded is %d — want smaller", len(rounded), len(raw))
	}
	if strings.Contains(string(rounded), "704.5835576057434") {
		t.Error("a full-precision magnitude survived into the emitted payload")
	}
}

// TestRoundScenariosForEmission_PassesNonScenarioKeysThrough: the payload is a
// map[string]any holding scenario slices under "city"/"bbox" alongside a
// "summary" map of counts. Only the slices are rounded; anything else must
// survive untouched, or the summary silently loses fields.
func TestRoundScenariosForEmission_PassesNonScenarioKeysThrough(t *testing.T) {
	summary := map[string]any{"city_count": 3, "city_pct": 0.87654321}
	in := map[string]any{
		"summary": summary,
		"city":    []forecast.ScenarioResult{{Years: []forecast.ScenarioYear{{Year: 1, AnnualNeed: 1234567.8901234567}}}},
	}

	got := RoundScenariosForEmission(in)

	gotSummary, ok := got["summary"].(map[string]any)
	if !ok {
		t.Fatalf("summary = %T, want map[string]any", got["summary"])
	}
	if gotSummary["city_count"] != 3 || gotSummary["city_pct"] != 0.87654321 {
		t.Errorf("summary altered: %v", gotSummary)
	}
	city, ok := got["city"].([]forecast.ScenarioResult)
	if !ok {
		t.Fatalf("city = %T, want []forecast.ScenarioResult", got["city"])
	}
	if city[0].Years[0].AnnualNeed != 1234568 {
		t.Errorf("city scenarios not rounded: %v", city[0].Years[0].AnnualNeed)
	}
	// Input untouched, same reasoning as the forecast case.
	orig := in["city"].([]forecast.ScenarioResult)
	if orig[0].Years[0].AnnualNeed != 1234567.8901234567 {
		t.Errorf("input scenarios were mutated: %v", orig[0].Years[0].AnnualNeed)
	}
}

// TestRoundTo_NonFinitePassthrough: a NaN or Inf must survive as-is rather than
// become 0. Emitting a full-precision value is always safe; emitting a silently
// zeroed dollar figure is not. Mirrors roundSig's contract.
func TestRoundTo_NonFinitePassthrough(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), 0} {
		if got := roundTo(v, 2); !(math.IsNaN(v) && math.IsNaN(got)) && got != v {
			t.Errorf("roundTo(%v) = %v, want %v", v, got, v)
		}
	}
}
