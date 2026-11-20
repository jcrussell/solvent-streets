package forecast

import (
	"encoding/json"
	"testing"
)

// A strategy is its snake_case name in JSON, and reads back from that name,
// the kebab-case and run-together spellings, and the integer older files hold.
func TestStrategy_JSON(t *testing.T) {
	for s, want := range map[Strategy]string{
		StrategyDoNothing:       `"do_nothing"`,
		StrategyWorstFirst:      `"worst_first"`,
		StrategyPreventiveFirst: `"preventive_first"`,
	} {
		got, err := json.Marshal(s)
		if err != nil || string(got) != want {
			t.Errorf("Marshal(%d) = %s, %v; want %s", int(s), got, err, want)
		}
	}
	for in, want := range map[string]Strategy{
		`"worst_first"`:      StrategyWorstFirst,
		`"worst-first"`:      StrategyWorstFirst,
		`"WorstFirst"`:       StrategyWorstFirst,
		`"do-nothing"`:       StrategyDoNothing,
		`0`:                  StrategyDoNothing,
		`1`:                  StrategyWorstFirst,
		`2`:                  StrategyPreventiveFirst,
		`"preventive_first"`: StrategyPreventiveFirst,
	} {
		var got Strategy
		if err := json.Unmarshal([]byte(in), &got); err != nil || got != want {
			t.Errorf("Unmarshal(%s) = %v, %v; want %v", in, got, err, want)
		}
	}
	for in, want := range map[string]string{
		`3`:          "unknown strategy 3",
		`-1`:         "unknown strategy -1",
		`1.5`:        "unknown strategy 1.5",
		`true`:       "unknown strategy true",
		`"sideways"`: `unknown strategy "sideways"`,
	} {
		var got Strategy
		if err := json.Unmarshal([]byte(in), &got); err == nil || err.Error() != want {
			t.Errorf("Unmarshal(%s) = %v, %v; want error %q", in, got, err, want)
		}
	}
	if _, err := json.Marshal(Strategy(7)); err == nil {
		t.Error("Marshal(Strategy(7)) succeeded; want an error")
	}
}

// annual_budget is left out where it does not apply: a do-nothing or a
// full-funding scenario. A budgeted scenario keeps it, even at $0.
func TestScenario_JSONOmitsBudgetThatDoesNotApply(t *testing.T) {
	for _, tc := range []struct {
		s    Scenario
		want string
	}{
		{BaselineScenario(), `{"name":"baseline","label":"Baseline (Do Nothing)","strategy":"do_nothing"}`},
		{Scenario{Name: "fund-100pct", Label: "Full Funding", FullFunding: true, Strategy: StrategyWorstFirst},
			`{"name":"fund-100pct","label":"Full Funding","full_funding":true,"strategy":"worst_first"}`},
		{Scenario{Name: "custom", Label: "Custom", AnnualBudget: 0, Strategy: StrategyPreventiveFirst},
			`{"name":"custom","label":"Custom","annual_budget":0,"strategy":"preventive_first"}`},
		{Scenario{Name: "fund-50pct", Label: "50% Funding", AnnualBudget: 1234, Strategy: StrategyWorstFirst},
			`{"name":"fund-50pct","label":"50% Funding","annual_budget":1234,"strategy":"worst_first"}`},
	} {
		got, err := json.Marshal(tc.s)
		if err != nil || string(got) != tc.want {
			t.Errorf("Marshal(%+v) =\n  %s, %v\nwant\n  %s", tc.s, got, err, tc.want)
		}
		var back Scenario
		if err := json.Unmarshal(got, &back); err != nil || back != tc.s {
			t.Errorf("round trip of %s = %+v, %v; want %+v", got, back, err, tc.s)
		}
	}
}
