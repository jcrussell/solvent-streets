package game

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/forecast"
)

func defaultTiers() []forecast.CostTier {
	// Copy of forecast.DefaultCostTiers (preventive/rehab/reconstruction).
	return []forecast.CostTier{
		{MinPCI: 70, MaxPCI: 101, CostPerSqM: 5.00, Label: "preventive"},
		{MinPCI: 40, MaxPCI: 70, CostPerSqM: 50.00, Label: "rehab"},
		{MinPCI: 0, MaxPCI: 40, CostPerSqM: 150.00, Label: "reconstruction"},
	}
}

func baseConfig() Config {
	return Config{
		Hexes: []HexConfig{
			{ID: "a", RoadArea: 1000, K: 0.04},
			{ID: "b", RoadArea: 2000, K: 0.02},
		},
		InitialPCI:          80,
		PCIJitter:           0,
		CostTiers:           defaultTiers(),
		StartingBudget:      1_000_000,
		HorizonYears:        20,
		TreatmentCycleYears: 12,
		GrowthRate:          0,
		Cohorts: []CohortConfig{
			{Classification: "residential", Area: 1000, DecayRate: 0.04},
			{Classification: "primary", Area: 2000, DecayRate: 0.02},
		},
	}
}

func newGame(t *testing.T, cfg Config) *Game {
	t.Helper()
	g, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g
}

func TestNewValidation(t *testing.T) {
	cases := map[string]func(*Config){
		"no hexes":      func(c *Config) { c.Hexes = nil },
		"bad k":         func(c *Config) { c.Hexes[0].K = 0 },
		"neg area":      func(c *Config) { c.Hexes[0].RoadArea = -1 },
		"bad horizon":   func(c *Config) { c.HorizonYears = 0 },
		"no cost tiers": func(c *Config) { c.CostTiers = nil },
		"empty id":      func(c *Config) { c.Hexes[0].ID = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := baseConfig()
			mutate(&cfg)
			if _, err := New(cfg); err == nil {
				t.Fatalf("expected error for %q", name)
			}
		})
	}
}

// TestDecayComposition asserts that many small ticks summing to one year land
// exactly on the single annual exp step (prod exp(-k*dt) == exp(-k*1)).
func TestDecayComposition(t *testing.T) {
	cfg := baseConfig()
	cfg.StartingBudget = 0 // no treasury growth to confuse anything
	g := newGame(t, cfg)

	startPCI := g.hexes[0].pci
	k := g.hexes[0].k

	const steps = 365
	for range steps {
		g.Tick(1.0 / steps)
	}

	want := startPCI * math.Exp(-k*1.0)
	got := g.hexes[0].pci
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("decay composition: got %.12f want %.12f", got, want)
	}
}

func TestJitterDeterministic(t *testing.T) {
	cfg := baseConfig()
	cfg.PCIJitter = 10
	g1 := newGame(t, cfg)
	g2 := newGame(t, cfg)
	for i := range g1.hexes {
		if g1.hexes[i].pci != g2.hexes[i].pci {
			t.Fatalf("jitter not deterministic for hex %d", i)
		}
		// within the jitter band
		if d := math.Abs(g1.hexes[i].pci - 80); d > 10+1e-9 {
			t.Fatalf("jitter out of band: %v", d)
		}
	}
	// jitter must actually vary the two hexes (different ids)
	if g1.hexes[0].pci == g1.hexes[1].pci {
		t.Fatalf("expected distinct jittered PCIs for distinct ids")
	}
}

func TestTreatDebitsLiftsAndReopens(t *testing.T) {
	cfg := baseConfig()
	g := newGame(t, cfg)

	// Force hex "a" to gravel.
	g.hexes[0].pci = 0
	g.hexes[0].closed = true

	treasuryBefore := g.treasury
	if !g.Treat("a", "reconstruction") {
		t.Fatal("reconstruction should be affordable")
	}
	cost := 150.0 * 1000.0
	if math.Abs((treasuryBefore-g.treasury)-cost) > 1e-6 {
		t.Fatalf("debit wrong: got %v want %v", treasuryBefore-g.treasury, cost)
	}
	if g.hexes[0].closed {
		t.Fatal("reconstruction must reopen a closed hex")
	}
	if g.hexes[0].pci < 99.999 {
		t.Fatalf("reconstruction should restore ~100, got %v", g.hexes[0].pci)
	}
}

func TestTreatPreventiveSmallLift(t *testing.T) {
	g := newGame(t, baseConfig())
	before := g.hexes[1].pci // 80
	if !g.Treat("b", "preventive") {
		t.Fatal("preventive should be affordable")
	}
	after := g.hexes[1].pci
	if after <= before {
		t.Fatal("preventive should lift PCI")
	}
	// preventive lift = (100-80)*1/3 ~ 6.67, much smaller than reconstruction.
	if after-before > 10 {
		t.Fatalf("preventive lift too large: %v", after-before)
	}
}

func TestTreatUnaffordableRejected(t *testing.T) {
	cfg := baseConfig()
	cfg.StartingBudget = 100 // tiny treasury
	g := newGame(t, cfg)
	if g.Treat("a", "reconstruction") {
		t.Fatal("expected rejection when unaffordable")
	}
	if g.treasury != 100 {
		t.Fatalf("treasury must be untouched on rejection, got %v", g.treasury)
	}
}

func TestTreatUnknownTierOrHex(t *testing.T) {
	g := newGame(t, baseConfig())
	if g.Treat("a", "nope") {
		t.Fatal("unknown tier should be rejected")
	}
	if g.Treat("zzz", "preventive") {
		t.Fatal("unknown hex should be rejected")
	}
}

func TestClosureAtZero(t *testing.T) {
	cfg := baseConfig()
	cfg.Hexes = []HexConfig{{ID: "a", RoadArea: 1000, K: 5.0}} // very fast decay
	cfg.InitialPCI = 2
	cfg.Cohorts = nil
	g := newGame(t, cfg)
	for i := 0; i < 50 && !g.hexes[0].closed; i++ {
		g.Tick(1)
	}
	if !g.hexes[0].closed {
		t.Fatal("hex should have closed")
	}
	if g.hexes[0].pci != 0 {
		t.Fatalf("closed hex PCI should be 0, got %v", g.hexes[0].pci)
	}
}

// TestTickNonFiniteIsNoOp asserts that a NaN/+Inf/-Inf dt is rejected (state
// unchanged and finite), since a bare dt<=0 guard lets them through.
func TestTickNonFiniteIsNoOp(t *testing.T) {
	for _, dt := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		t.Run(fmt.Sprintf("dt=%v", dt), func(t *testing.T) {
			g := newGame(t, baseConfig())
			yearBefore := g.simYears
			treasuryBefore := g.treasury
			pciBefore := make([]float64, len(g.hexes))
			for i := range g.hexes {
				pciBefore[i] = g.hexes[i].pci
			}

			g.Tick(dt)

			if g.simYears != yearBefore {
				t.Fatalf("simYears changed: got %v want %v", g.simYears, yearBefore)
			}
			if g.treasury != treasuryBefore {
				t.Fatalf("treasury changed: got %v want %v", g.treasury, treasuryBefore)
			}
			if math.IsNaN(g.simYears) || math.IsNaN(g.treasury) {
				t.Fatal("state became NaN")
			}
			for i := range g.hexes {
				if g.hexes[i].pci != pciBefore[i] || math.IsNaN(g.hexes[i].pci) {
					t.Fatalf("hex %d pci changed/NaN: got %v want %v", i, g.hexes[i].pci, pciBefore[i])
				}
			}
		})
	}
}

// TestSetBudgetNonFiniteIgnored asserts a NaN/Inf rate is ignored (budgetRate
// untouched) so it cannot poison the treasury on the next Tick.
func TestSetBudgetNonFiniteIgnored(t *testing.T) {
	for _, rate := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		g := newGame(t, baseConfig())
		rateBefore := g.budgetRate
		g.SetBudget(rate)
		if g.budgetRate != rateBefore {
			t.Fatalf("non-finite rate %v changed budgetRate to %v", rate, g.budgetRate)
		}
		g.Tick(1)
		if math.IsNaN(g.treasury) || math.IsInf(g.treasury, 0) {
			t.Fatalf("treasury non-finite after Tick: %v", g.treasury)
		}
	}
}

// TestSetBudgetNegativeClamped asserts a negative rate is clamped to 0 (no debt
// model), so the treasury does not bleed below zero over ticks.
func TestSetBudgetNegativeClamped(t *testing.T) {
	g := newGame(t, baseConfig())
	g.SetBudget(-1_000_000)
	if g.budgetRate != 0 {
		t.Fatalf("negative budget should clamp to 0, got %v", g.budgetRate)
	}
	treasuryBefore := g.treasury
	g.Tick(1)
	if g.treasury < treasuryBefore {
		t.Fatalf("treasury bled with clamped budget: %v < %v", g.treasury, treasuryBefore)
	}
}

func TestSetBudgetChangesInsolvencyYear(t *testing.T) {
	g := newGame(t, baseConfig())

	g.SetBudget(0) // do-nothing-ish: should be insolvent within horizon
	lowYear, lowOK := g.insolvencyYear, g.insolvencyOK
	if !lowOK {
		t.Fatal("expected insolvency within horizon at budget 0")
	}

	g.SetBudget(1e12) // massive budget: should stay solvent through horizon
	if g.insolvencyOK {
		t.Fatalf("expected solvent-through-horizon at huge budget, got year %d", g.insolvencyYear)
	}

	// And a modest budget should push insolvency later than budget 0 (or solvent).
	g.SetBudget(500_000)
	if g.insolvencyOK && g.insolvencyYear < lowYear {
		t.Fatalf("more budget should not make insolvency earlier: %d < %d", g.insolvencyYear, lowYear)
	}
}

// TestProjectInsolvency asserts the board-free pregame projection agrees with
// the live (*Game).recomputeInsolvency verdict and moves the right way with
// budget: a starving budget is insolvent within the horizon, an ample budget is
// solvent, and it needs no hexes (pregame has cohorts but no board yet).
func TestProjectInsolvency(t *testing.T) {
	cfg := baseConfig()

	cfg.StartingBudget = 0
	year, ok := ProjectInsolvency(cfg)
	if !ok {
		t.Fatal("expected insolvency within horizon at budget 0")
	}
	if year <= 0 || year > int(cfg.HorizonYears) {
		t.Fatalf("insolvency year %d out of range (1..%d)", year, int(cfg.HorizonYears))
	}

	cfg.StartingBudget = 1e12
	if _, ok := ProjectInsolvency(cfg); ok {
		t.Fatal("expected solvent-through-horizon at a huge budget")
	}

	// It must match the live path: a game built at the same starving budget
	// caches the same verdict recomputeInsolvency produces.
	cfg.StartingBudget = 0
	g := newGame(t, cfg)
	pYear, pOK := ProjectInsolvency(cfg)
	if pOK != g.insolvencyOK || (pOK && pYear != g.insolvencyYear) {
		t.Fatalf("ProjectInsolvency (%d,%v) != recomputeInsolvency (%d,%v)",
			pYear, pOK, g.insolvencyYear, g.insolvencyOK)
	}

	// No board required: cohorts drive the projection, hexes are ignored.
	cfg.Hexes = nil
	if _, ok := ProjectInsolvency(cfg); !ok {
		t.Fatal("expected insolvency at budget 0 even with no hexes")
	}
}

func TestWinTransition(t *testing.T) {
	cfg := baseConfig()
	cfg.HorizonYears = 2
	cfg.Hexes = []HexConfig{{ID: "a", RoadArea: 1000, K: 0.0001}} // barely decays
	cfg.InitialPCI = 95
	cfg.Cohorts = nil
	g := newGame(t, cfg)
	g.Tick(1)
	if g.status != "running" {
		t.Fatalf("should still be running at year 1, got %q", g.status)
	}
	g.Tick(1.5)
	if g.status != "won" {
		t.Fatalf("should have won at horizon, got %q", g.status)
	}
	// Terminal: further ticks do not change status.
	g.Tick(1)
	if g.status != "won" {
		t.Fatalf("won must be terminal, got %q", g.status)
	}
}

func TestLoseTransition(t *testing.T) {
	cfg := baseConfig()
	cfg.HorizonYears = 100
	cfg.Hexes = []HexConfig{
		{ID: "a", RoadArea: 1000, K: 1.0},
		{ID: "b", RoadArea: 1000, K: 1.0},
	}
	cfg.InitialPCI = 40
	cfg.StartingBudget = 0
	cfg.Cohorts = nil
	g := newGame(t, cfg)
	for i := 0; i < 100 && g.status == "running"; i++ {
		g.Tick(1)
	}
	if g.status != "lost" {
		t.Fatalf("expected lost, got %q", g.status)
	}
}

func TestStateJSONDelta(t *testing.T) {
	g := newGame(t, baseConfig())

	// First call: every hex emitted (initial paint).
	first := decodeState(t, g)
	if len(first.Changed) != len(g.hexes) {
		t.Fatalf("first StateJSON should emit all %d hexes, got %d", len(g.hexes), len(first.Changed))
	}

	// Second call with no change: empty delta.
	second := decodeState(t, g)
	if len(second.Changed) != 0 {
		t.Fatalf("no-change StateJSON should emit 0 hexes, got %d", len(second.Changed))
	}

	// Decay hex "a" across a band boundary; only it should appear.
	// Drive its band down.
	prevBand := BandForPCI(g.hexes[0].pci)
	for i := 0; i < 200 && BandForPCI(g.hexes[0].pci) == prevBand; i++ {
		g.Tick(0.5)
	}
	third := decodeState(t, g)
	if len(third.Changed) == 0 {
		t.Fatal("expected at least one changed hex after a band crossing")
	}
	for _, c := range third.Changed {
		if c.ID != "a" && c.ID != "b" {
			t.Fatalf("unexpected changed id %q", c.ID)
		}
	}
}

func TestStateJSONShape(t *testing.T) {
	g := newGame(t, baseConfig())
	raw, err := g.StateJSON()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"year", "treasury", "budget_rate", "network_pci", "backlog", "insolvency_year", "status", "changed"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("StateJSON missing key %q", k)
		}
	}
}

func TestInsolvencyNullWhenSolvent(t *testing.T) {
	g := newGame(t, baseConfig())
	g.SetBudget(1e12)
	st := decodeState(t, g)
	if st.InsolvencyYear != nil {
		t.Fatalf("expected null insolvency_year when solvent, got %v", *st.InsolvencyYear)
	}
}

func TestBandForPCI(t *testing.T) {
	if BandForPCI(0) != 0 {
		t.Fatal("0 -> band 0")
	}
	if BandForPCI(100) != BandCount-1 {
		t.Fatalf("100 -> band %d", BandCount-1)
	}
	if BandForPCI(-5) != 0 {
		t.Fatal("negative -> band 0")
	}
	// monotonic non-decreasing
	prev := 0
	for p := 0.0; p <= 100; p++ {
		b := BandForPCI(p)
		if b < prev {
			t.Fatalf("band not monotonic at pci %v", p)
		}
		prev = b
	}
}

// --- helpers ---

type stateView struct {
	Year           float64      `json:"year"`
	Treasury       float64      `json:"treasury"`
	BudgetRate     float64      `json:"budget_rate"`
	NetworkPCI     float64      `json:"network_pci"`
	Backlog        float64      `json:"backlog"`
	InsolvencyYear *int         `json:"insolvency_year"`
	Status         string       `json:"status"`
	Spent          float64      `json:"spent"`
	Treatments     int          `json:"treatments"`
	ClosedCount    int          `json:"closed_count"`
	Changed        []changedHex `json:"changed"`
}

func decodeState(t *testing.T, g *Game) stateView {
	t.Helper()
	raw, err := g.StateJSON()
	if err != nil {
		t.Fatal(err)
	}
	var st stateView
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

// TestAutoTierSelection covers the "auto" rule the paint brush relies on: each
// PCI maps to the tier whose [MinPCI, MaxPCI) band contains it, including the
// inclusive-min / exclusive-max boundaries and the PCI=100 sentinel case.
func TestAutoTierSelection(t *testing.T) {
	g := newGame(t, baseConfig()) // preventive[70,101) rehab[40,70) recon[0,40)
	cases := []struct {
		pci  float64
		want string
	}{
		{100, "preventive"},
		{70, "preventive"},
		{69.9, "rehab"},
		{40, "rehab"},
		{39.9, "reconstruction"},
		{0, "reconstruction"},
	}
	for _, c := range cases {
		i := g.autoTierIndex(c.pci)
		got := "none"
		if i >= 0 {
			got = g.tiers[i].Label
		}
		if got != c.want {
			t.Errorf("autoTierIndex(%g) = %s, want %s", c.pci, got, c.want)
		}
	}
}

// TestTreatAutoReopensClosedHex: "auto" on a gravel (closed, PCI 0) hex selects
// reconstruction and reopens it — the brush rebuilds failed roads with no
// special-casing.
func TestTreatAutoReopensClosedHex(t *testing.T) {
	g := newGame(t, baseConfig())
	g.hexes[0].pci = 0
	g.hexes[0].closed = true
	if !g.Treat("a", "auto") {
		t.Fatal("auto on a gravel hex should pick reconstruction and apply")
	}
	if g.hexes[0].closed {
		t.Fatal("auto/reconstruction must reopen the closed hex")
	}
	if g.hexes[0].pci < 99.999 {
		t.Fatalf("reconstruction should restore ~100, got %v", g.hexes[0].pci)
	}
}

// TestEndlessNeverWins: with Endless set the game never wins at the horizon,
// while an otherwise-identical finite-horizon game does.
func TestEndlessNeverWins(t *testing.T) {
	cfg := baseConfig()
	cfg.HorizonYears = 2
	cfg.Hexes = []HexConfig{{ID: "a", RoadArea: 1000, K: 0.0001}} // barely decays
	cfg.InitialPCI = 95
	cfg.Cohorts = nil

	cfg.Endless = true
	g := newGame(t, cfg)
	g.Tick(5) // well past the horizon
	if g.status != "running" {
		t.Fatalf("endless must keep running past the horizon, got %q", g.status)
	}

	cfg.Endless = false
	g2 := newGame(t, cfg)
	g2.Tick(5)
	if g2.status != "won" {
		t.Fatalf("finite horizon should win past the horizon, got %q", g2.status)
	}
}

// TestSpentAndTreatmentsAccounting: the summary totals accumulate on successful
// treats and ignore rejected (unaffordable) ones.
func TestSpentAndTreatmentsAccounting(t *testing.T) {
	g := newGame(t, baseConfig())
	if g.spent != 0 || g.treatments != 0 {
		t.Fatalf("fresh game must start at 0 spent/treatments, got %v/%d", g.spent, g.treatments)
	}
	g.Treat("a", "preventive") // 5 * 1000
	g.Treat("b", "preventive") // 5 * 2000
	if g.treatments != 2 || math.Abs(g.spent-15000) > 1e-6 {
		t.Fatalf("after 2 treats: spent=%v treatments=%d, want 15000/2", g.spent, g.treatments)
	}
	g.treasury = 0 // make the next treat unaffordable
	if g.Treat("a", "reconstruction") {
		t.Fatal("treat should be rejected with empty treasury")
	}
	if g.treatments != 2 || math.Abs(g.spent-15000) > 1e-6 {
		t.Fatalf("rejected treat must not change totals: spent=%v treatments=%d", g.spent, g.treatments)
	}
}

// TestTreatBatch applies many hexes with one status recompute, skipping unknown
// ids, and returns the count applied.
func TestTreatBatch(t *testing.T) {
	g := newGame(t, baseConfig())
	n := g.TreatBatch([]string{"a", "b", "zzz"}, "auto")
	if n != 2 {
		t.Fatalf("TreatBatch applied %d, want 2 (unknown id skipped)", n)
	}
	if g.treatments != 2 {
		t.Fatalf("treatments = %d, want 2", g.treatments)
	}
}

// TestStateJSONSummaryFields: the state delta carries the cumulative summary
// totals (spent/treatments) and the live closed_count.
func TestStateJSONSummaryFields(t *testing.T) {
	g := newGame(t, baseConfig())
	g.hexes[0].closed = true // 1 of 2 closed (under the 50% loss threshold)
	g.Treat("b", "preventive")
	st := decodeState(t, g)
	if st.ClosedCount != 1 {
		t.Fatalf("closed_count = %d, want 1", st.ClosedCount)
	}
	if st.Treatments != 1 || st.Spent <= 0 {
		t.Fatalf("summary fields wrong: spent=%v treatments=%d", st.Spent, st.Treatments)
	}
}
