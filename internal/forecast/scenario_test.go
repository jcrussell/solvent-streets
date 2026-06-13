package forecast

import (
	"testing"
)

func defaultTestParams() *Params {
	return NewParams(0.01, nil)
}

func singleCohort(area, decayRate float64) []Cohort {
	return []Cohort{{
		Classification: "default",
		Area:           area,
		DecayRate:      decayRate,
		InitialPCI:     85.0,
	}}
}

// TestSimulate_CostTierLabelHonorsCustomTiers locks in that the per-year
// CostTier label is derived from the configured tier set, not the hardcoded
// DefaultCostTiers, so the label never contradicts ProjectCost's dollar math.
func TestSimulate_CostTierLabelHonorsCustomTiers(t *testing.T) {
	// Custom labels distinct from the defaults (preventive/rehab/reconstruction),
	// spanning the full 0-101 range so every blended PCI matches a band.
	customTiers := []CostTier{
		{MinPCI: 0, MaxPCI: 25, CostPerSqM: 150, Label: "Failed"},
		{MinPCI: 25, MaxPCI: 50, CostPerSqM: 90, Label: "Poor"},
		{MinPCI: 50, MaxPCI: 70, CostPerSqM: 50, Label: "Fair"},
		{MinPCI: 70, MaxPCI: 101, CostPerSqM: 5, Label: "Good"},
	}
	params := NewParams(0.0, customTiers)
	cohorts := singleCohort(100000, 0.035)
	result := Simulate(Scenario{Name: "dn", Label: "DN", Strategy: StrategyDoNothing}, cohorts, 20, params)

	customLabels := map[string]bool{"Failed": true, "Poor": true, "Fair": true, "Good": true}
	for _, y := range result.Years {
		if !customLabels[y.CostTier] {
			t.Errorf("year %d: CostTier %q is not from the configured tier set", y.Year, y.CostTier)
		}
		// Cross-check the label against the band the PCI actually falls in.
		if want := TierForPCIIn(customTiers, y.PCI); want != "" && y.CostTier != want {
			t.Errorf("year %d: PCI %.1f labeled %q, want %q", y.Year, y.PCI, y.CostTier, want)
		}
	}
}

// TestTierForPCI_GapFallback ensures a PCI falling in a coverage gap of a
// custom tier set is labeled from that set's nearest band, never a hardcoded
// default-schedule label that may not exist in the operator's configuration.
func TestTierForPCI_GapFallback(t *testing.T) {
	// Gap between 40 and 70 — no band covers PCI 55.
	p := &TieredCostProjector{Tiers: []CostTier{
		{MinPCI: 0, MaxPCI: 40, CostPerSqM: 150, Label: "Failed"},
		{MinPCI: 70, MaxPCI: 101, CostPerSqM: 5, Label: "Good"},
	}}
	got := p.TierForPCI(55) // 15 below 70 ("Good"), 15 above 40 ("Failed"): tie -> first nearest
	if got != "Failed" && got != "Good" {
		t.Errorf("gap PCI labeled %q; want a label from the configured set", got)
	}
	if got := p.TierForPCI(68); got != "Good" {
		t.Errorf("PCI 68 (nearest to [70,101)) labeled %q, want Good", got)
	}
}

func TestSimulate_DoNothing_DecreasingPCI(t *testing.T) {
	params := defaultTestParams()
	cohorts := singleCohort(100000, 0.035)
	s := Scenario{Name: "test-dn", Label: "Test", Strategy: StrategyDoNothing}
	result := Simulate(s, cohorts, 20, params)

	if len(result.Years) != 20 {
		t.Fatalf("expected 20 years, got %d", len(result.Years))
	}

	for i := 1; i < len(result.Years); i++ {
		if result.Years[i].PCI >= result.Years[i-1].PCI {
			t.Errorf("PCI should decrease: year %d (%.2f) >= year %d (%.2f)",
				result.Years[i].Year, result.Years[i].PCI,
				result.Years[i-1].Year, result.Years[i-1].PCI)
		}
	}

	// Do-nothing should have zero spend
	for _, y := range result.Years {
		if y.AnnualSpend != 0 {
			t.Errorf("year %d: do-nothing should have 0 spend, got %.2f", y.Year, y.AnnualSpend)
		}
	}

	// Deferred backlog should grow
	for i := 1; i < len(result.Years); i++ {
		if result.Years[i].DeferredBacklog <= result.Years[i-1].DeferredBacklog {
			t.Errorf("deferred backlog should grow in do-nothing scenario")
		}
	}
}

func TestSimulate_Unconstrained_PCIRecovery(t *testing.T) {
	params := defaultTestParams()
	cohorts := singleCohort(100000, 0.035)
	s := Scenario{Name: "full", Label: "Full", FullFunding: true, Strategy: StrategyWorstFirst}
	result := Simulate(s, cohorts, 10, params)

	// With full funding, PCI should fully recover to initial value each year
	for _, y := range result.Years {
		if y.PCI != 85.0 {
			t.Errorf("year %d: full funding PCI should equal initial (85.0), got %.2f", y.Year, y.PCI)
		}
	}

	// Deferred backlog should be zero (spending = need)
	for _, y := range result.Years {
		if y.DeferredBacklog > 0.01 {
			t.Errorf("year %d: unconstrained should have ~0 backlog, got %.2f", y.Year, y.DeferredBacklog)
		}
	}
}

func TestSimulate_BudgetConstrained_Intermediate(t *testing.T) {
	params := defaultTestParams()
	cohorts := singleCohort(100000, 0.035)

	// Get year-1 need to set budget at 50%
	doNothing := Simulate(
		Scenario{Name: "dn", Label: "DN", Strategy: StrategyDoNothing},
		cohorts, 1, params,
	)
	year1Need := doNothing.Years[0].AnnualNeed

	constrained := Simulate(
		Scenario{Name: "half", Label: "Half", AnnualBudget: year1Need * 0.5, Strategy: StrategyWorstFirst},
		cohorts, 20, params,
	)

	full := Simulate(
		Scenario{Name: "full", Label: "Full", FullFunding: true, Strategy: StrategyWorstFirst},
		cohorts, 20, params,
	)

	lastConstrained := constrained.Years[19]
	lastFull := full.Years[19]

	// Budget-constrained should have worse PCI than full funding
	if lastConstrained.PCI >= lastFull.PCI {
		t.Errorf("constrained PCI (%.2f) should be worse than full (%.2f)",
			lastConstrained.PCI, lastFull.PCI)
	}

	// Budget-constrained should have positive deferred backlog
	if lastConstrained.DeferredBacklog <= 0 {
		t.Error("constrained scenario should have deferred backlog > 0")
	}
}

func TestSimulate_PreventiveOutperformsWorstFirst(t *testing.T) {
	params := defaultTestParams()
	cohorts := singleCohort(100000, 0.035)

	doNothing := Simulate(
		Scenario{Name: "dn", Label: "DN", Strategy: StrategyDoNothing},
		cohorts, 1, params,
	)
	budget := doNothing.Years[0].AnnualNeed * 0.5

	worst := Simulate(
		Scenario{Name: "worst", Label: "Worst", AnnualBudget: budget, Strategy: StrategyWorstFirst},
		cohorts, 20, params,
	)

	preventive := Simulate(
		Scenario{Name: "prev", Label: "Prev", AnnualBudget: budget, Strategy: StrategyPreventiveFirst},
		cohorts, 20, params,
	)

	lastWorst := worst.Years[19]
	lastPreventive := preventive.Years[19]

	if lastPreventive.PCI <= lastWorst.PCI {
		t.Errorf("preventive-first PCI (%.2f) should outperform worst-first (%.2f) at same budget",
			lastPreventive.PCI, lastWorst.PCI)
	}
}

func TestStrategy_StringRoundTrip(t *testing.T) {
	strategies := []Strategy{StrategyDoNothing, StrategyWorstFirst, StrategyPreventiveFirst}
	for _, s := range strategies {
		str := s.String()
		parsed, err := ParseStrategy(str)
		if err != nil {
			t.Errorf("ParseStrategy(%q): %v", str, err)
			continue
		}
		if parsed != s {
			t.Errorf("round-trip failed: %v -> %q -> %v", s, str, parsed)
		}
	}
}

func TestParseStrategy_Invalid(t *testing.T) {
	_, err := ParseStrategy("invalid-strategy")
	if err == nil {
		t.Error("expected error for invalid strategy")
	}
}

func TestSimulate_Overfunding_SpendExceedsFullFunding(t *testing.T) {
	params := defaultTestParams()
	cohorts := singleCohort(100000, 0.035)

	// Get year-1 need to calibrate budget
	doNothing := Simulate(
		Scenario{Name: "dn", Label: "DN", Strategy: StrategyDoNothing},
		cohorts, 1, params,
	)
	year1Need := doNothing.Years[0].AnnualNeed

	full := Simulate(
		Scenario{Name: "full", Label: "Full", FullFunding: true, Strategy: StrategyWorstFirst},
		cohorts, 20, params,
	)

	over := Simulate(
		Scenario{Name: "over", Label: "150%", AnnualBudget: year1Need * 1.5, Strategy: StrategyWorstFirst},
		cohorts, 20, params,
	)

	// Cumulative spend for overfunding should exceed full funding
	var cumFull, cumOver float64
	for i := range full.Years {
		cumFull += full.Years[i].AnnualSpend
		cumOver += over.Years[i].AnnualSpend
	}
	if cumOver <= cumFull {
		t.Errorf("150%% cumulative spend (%.2f) should exceed full funding (%.2f)", cumOver, cumFull)
	}

	// Overfunding should improve PCI above full funding's stable level
	if over.Years[19].PCI <= full.Years[19].PCI {
		t.Errorf("150%% PCI (%.2f) should exceed full funding PCI (%.2f)", over.Years[19].PCI, full.Years[19].PCI)
	}

	// Deferred backlog should be zero (no unmet need)
	for _, y := range over.Years {
		if y.DeferredBacklog > 0.01 {
			t.Errorf("year %d: overfunding should have ~0 backlog, got %.2f", y.Year, y.DeferredBacklog)
		}
	}
}

func TestDefaultComparisons(t *testing.T) {
	scenarios := DefaultComparisons(1000000)
	if len(scenarios) != 3 {
		t.Fatalf("expected 3 default scenarios, got %d", len(scenarios))
	}

	// First should be 25% funding (do-nothing is created separately as baseline)
	if scenarios[0].Name != "fund-25pct" {
		t.Errorf("first scenario should be fund-25pct, got %s", scenarios[0].Name)
	}

	// Check budget levels
	if scenarios[0].AnnualBudget != 250000 {
		t.Errorf("25%% funding should be 250000, got %.0f", scenarios[0].AnnualBudget)
	}
}

func TestSimulate_TwoCohorts_BlendedPCI(t *testing.T) {
	params := defaultTestParams()
	cohorts := []Cohort{
		{Classification: "primary", Area: 50000, DecayRate: 0.025, InitialPCI: 85.0},
		{Classification: "residential", Area: 50000, DecayRate: 0.040, InitialPCI: 85.0},
	}

	result := Simulate(
		Scenario{Name: "dn", Label: "DN", Strategy: StrategyDoNothing},
		cohorts, 20, params,
	)

	// Blended PCI should be between the two individual decay trajectories
	// Primary decays slower, residential faster
	primaryOnly := Simulate(
		Scenario{Name: "dn", Label: "DN", Strategy: StrategyDoNothing},
		singleCohort(50000, 0.025), 20, params,
	)
	residentialOnly := Simulate(
		Scenario{Name: "dn", Label: "DN", Strategy: StrategyDoNothing},
		singleCohort(50000, 0.040), 20, params,
	)

	lastBlended := result.Years[19].PCI
	lastPrimary := primaryOnly.Years[19].PCI
	lastResidential := residentialOnly.Years[19].PCI

	if lastBlended >= lastPrimary || lastBlended <= lastResidential {
		t.Errorf("blended PCI (%.2f) should be between primary (%.2f) and residential (%.2f)",
			lastBlended, lastPrimary, lastResidential)
	}

	// FinalCohorts should be populated
	if len(result.FinalCohorts) != 2 {
		t.Fatalf("expected 2 final cohorts, got %d", len(result.FinalCohorts))
	}
	if result.FinalCohorts[0].Classification != "primary" {
		t.Errorf("expected first cohort to be primary, got %s", result.FinalCohorts[0].Classification)
	}
	// Primary should have higher end PCI than residential
	if result.FinalCohorts[0].EndPCI <= result.FinalCohorts[1].EndPCI {
		t.Errorf("primary end PCI (%.2f) should exceed residential (%.2f)",
			result.FinalCohorts[0].EndPCI, result.FinalCohorts[1].EndPCI)
	}
}

func TestSimulate_TwoCohorts_BudgetProportionalToNeed(t *testing.T) {
	params := defaultTestParams()
	// Residential decays faster → higher need → gets more budget
	cohorts := []Cohort{
		{Classification: "primary", Area: 50000, DecayRate: 0.025, InitialPCI: 85.0},
		{Classification: "residential", Area: 50000, DecayRate: 0.040, InitialPCI: 85.0},
	}

	doNothing := Simulate(
		Scenario{Name: "dn", Label: "DN", Strategy: StrategyDoNothing},
		cohorts, 1, params,
	)
	budget := doNothing.Years[0].AnnualNeed * 0.5

	constrained := Simulate(
		Scenario{Name: "half", Label: "Half", AnnualBudget: budget, Strategy: StrategyWorstFirst},
		cohorts, 20, params,
	)

	// Should not crash and should produce valid results
	if len(constrained.Years) != 20 {
		t.Fatalf("expected 20 years, got %d", len(constrained.Years))
	}
	// PCI should be between 0 and 100
	for _, y := range constrained.Years {
		if y.PCI < 0 || y.PCI > 100 {
			t.Errorf("year %d: PCI (%.2f) out of range", y.Year, y.PCI)
		}
	}
}

func TestNormalizeClass(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"motorway", "motorway"},
		{"motorway_link", "motorway"},
		{"primary_link", "primary"},
		{"residential", "residential"},
		{"living_street", "residential"},
		{"unclassified", "residential"},
		{"something_unknown", "residential"},
		{"trunk", "trunk"},
		{"service", "service"},
	}
	for _, tt := range tests {
		got := NormalizeClass(tt.input)
		if got != tt.want {
			t.Errorf("NormalizeClass(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBuildCohorts_WithOverride(t *testing.T) {
	stats := []CohortInput{
		{Classification: "primary", Area: 50000},
		{Classification: "residential", Area: 30000},
	}

	cohorts := BuildCohorts(stats, 85.0, 0.05)
	if len(cohorts) != 2 {
		t.Fatalf("expected 2 cohorts, got %d", len(cohorts))
	}
	for _, c := range cohorts {
		if c.DecayRate != 0.05 {
			t.Errorf("cohort %s: expected decay rate 0.05, got %f", c.Classification, c.DecayRate)
		}
		if c.InitialPCI != 85.0 {
			t.Errorf("cohort %s: expected initial PCI 85.0, got %f", c.Classification, c.InitialPCI)
		}
	}
}

func TestBuildCohorts_WithoutOverride(t *testing.T) {
	stats := []CohortInput{
		{Classification: "primary", Area: 50000},
		{Classification: "residential", Area: 30000},
	}

	cohorts := BuildCohorts(stats, 85.0, 0)
	if len(cohorts) != 2 {
		t.Fatalf("expected 2 cohorts, got %d", len(cohorts))
	}
	if cohorts[0].DecayRate != DefaultDecayRates["primary"] {
		t.Errorf("primary: expected decay rate %f, got %f", DefaultDecayRates["primary"], cohorts[0].DecayRate)
	}
	if cohorts[1].DecayRate != DefaultDecayRates["residential"] {
		t.Errorf("residential: expected decay rate %f, got %f", DefaultDecayRates["residential"], cohorts[1].DecayRate)
	}
}

func TestBuildCohorts_Empty(t *testing.T) {
	cohorts := BuildCohorts(nil, 85.0, 0)
	if cohorts != nil {
		t.Errorf("expected nil for empty stats, got %v", cohorts)
	}
}

func TestIsRoadClass(t *testing.T) {
	roads := []string{"motorway", "trunk", "primary", "secondary", "tertiary", "residential", "service", "roads"}
	for _, c := range roads {
		if !IsRoadClass(c) {
			t.Errorf("IsRoadClass(%q) = false, want true", c)
		}
	}
	nonRoads := []string{"parking", "sidewalks", "sidewalk", "unknown", ""}
	for _, c := range nonRoads {
		if IsRoadClass(c) {
			t.Errorf("IsRoadClass(%q) = true, want false", c)
		}
	}
}

func TestBuildCohorts_MixedOverride(t *testing.T) {
	stats := []CohortInput{
		{Classification: "primary", Area: 50000},
		{Classification: "sidewalks", Area: 10000},
	}

	cohorts := BuildCohorts(stats, 85.0, 0.05)
	if len(cohorts) != 2 {
		t.Fatalf("expected 2 cohorts, got %d", len(cohorts))
	}
	if cohorts[0].DecayRate != 0.05 {
		t.Errorf("primary: expected override rate 0.05, got %f", cohorts[0].DecayRate)
	}
	// The growth-rate override applies only to road classes, so sidewalks keep
	// their class-specific rate. The decay table is keyed on the plural
	// resource-type string ("sidewalks"), matching what compute persists.
	if cohorts[1].DecayRate != DefaultDecayRates["sidewalks"] {
		t.Errorf("sidewalks: expected class rate %f, got %f", DefaultDecayRates["sidewalks"], cohorts[1].DecayRate)
	}
}
