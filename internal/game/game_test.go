package game

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/forecast"
)

// defaultTiers returns a fresh copy of forecast.DefaultCostTiers so tests
// exercise the real cost model (and tier tuning shows up here), while mutating
// tests can't alias the shared package-level slice.
func defaultTiers() []forecast.CostTier {
	tiers := make([]forecast.CostTier, len(forecast.DefaultCostTiers))
	copy(tiers, forecast.DefaultCostTiers)
	return tiers
}

func baseConfig() Config {
	return Config{
		Hexes: []HexConfig{
			{ID: "a", RoadsArea: 1000, DecayRate: 0.04},
			{ID: "b", RoadsArea: 2000, DecayRate: 0.02},
		},
		InitialPCI:          80,
		PCIJitter:           new(0.0),
		CostTiers:           defaultTiers(),
		AnnualBudget:        1_000_000,
		HorizonYears:        20,
		TreatmentCycleYears: 12,
		GrowthRate:          0,
		Cohorts: []CohortConfig{
			{Classification: "residential", Area: 1000, DecayRate: 0.04},
			{Classification: "primary", Area: 2000, DecayRate: 0.02},
		},
	}
}

// treat applies one treatment through TreatBatch, the engine's only treatment
// entry point, and reports whether it was applied.
func treat(g *Game, hexID, tier string) bool {
	return g.TreatBatch([]string{hexID}, tier) == 1
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
		"no hexes": func(c *Config) { c.Hexes = nil },
		"bad k":    func(c *Config) { c.Hexes[0].DecayRate = 0 },
		"neg area": func(c *Config) { c.Hexes[0].RoadsArea = -1 },
		"no road area": func(c *Config) {
			for i := range c.Hexes {
				c.Hexes[i].RoadsArea = 0
			}
		},
		"bad horizon":     func(c *Config) { c.HorizonYears = 0 },
		"no cost tiers":   func(c *Config) { c.CostTiers = nil },
		"empty id":        func(c *Config) { c.Hexes[0].ID = "" },
		"nan horizon":     func(c *Config) { c.HorizonYears = math.NaN() },
		"inf horizon":     func(c *Config) { c.HorizonYears = math.Inf(1) },
		"nan k":           func(c *Config) { c.Hexes[0].DecayRate = math.NaN() },
		"inf area":        func(c *Config) { c.Hexes[0].RoadsArea = math.Inf(1) },
		"nan initialpci":  func(c *Config) { c.InitialPCI = math.NaN() },
		"initialpci>100":  func(c *Config) { c.InitialPCI = 101 },
		"neg initialpci":  func(c *Config) { c.InitialPCI = -1 },
		"nan jitter":      func(c *Config) { c.PCIJitter = new(math.NaN()) },
		"neg jitter":      func(c *Config) { c.PCIJitter = new(-1.0) },
		"nan budget":      func(c *Config) { c.AnnualBudget = math.NaN() },
		"inf budget":      func(c *Config) { c.AnnualBudget = math.Inf(-1) },
		"nan growth":      func(c *Config) { c.GrowthRate = math.NaN() },
		"nan multiplier":  func(c *Config) { c.CostMultiplier = math.NaN() },
		"neg multiplier":  func(c *Config) { c.CostMultiplier = -1 },
		"huge multiplier": func(c *Config) { c.CostMultiplier = forecast.MaxCostMultiplier + 1 },
		"neg tier cost":   func(c *Config) { c.CostTiers[0].CostPerSqM = -1 },
		"nan tier cost":   func(c *Config) { c.CostTiers[0].CostPerSqM = math.NaN() },
		"nan cohort area": func(c *Config) {
			c.Cohorts[0].Area = math.NaN()
		},
		"neg cohort area":  func(c *Config) { c.Cohorts[0].Area = -1 },
		"inf cohort decay": func(c *Config) { c.Cohorts[0].DecayRate = math.Inf(1) },
		"neg cohort decay": func(c *Config) { c.Cohorts[0].DecayRate = -1 },
		// ResolveCycleYears only maps <= 0 to the default, so a sub-annual
		// positive cycle reaches Simulate and forces insolvency in year 1 at any
		// budget. New must reject it before that.
		"nan cycle":  func(c *Config) { c.TreatmentCycleYears = math.NaN() },
		"inf cycle":  func(c *Config) { c.TreatmentCycleYears = math.Inf(1) },
		"tiny cycle": func(c *Config) { c.TreatmentCycleYears = 1e-9 },
		"neg cycle":  func(c *Config) { c.TreatmentCycleYears = -1 },
		"huge cycle": func(c *Config) { c.TreatmentCycleYears = 41 },
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
	cfg.AnnualBudget = 0 // no treasury growth to confuse anything
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
	cfg.PCIJitter = new(10.0)
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
	if !treat(g, "a", "reconstruction") {
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
	if !treat(g, "b", "preventive") {
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
	cfg.AnnualBudget = 100 // tiny treasury
	g := newGame(t, cfg)
	if treat(g, "a", "reconstruction") {
		t.Fatal("expected rejection when unaffordable")
	}
	if g.treasury != 100 {
		t.Fatalf("treasury must be untouched on rejection, got %v", g.treasury)
	}
}

func TestTreatUnknownTierOrHex(t *testing.T) {
	g := newGame(t, baseConfig())
	if treat(g, "a", "nope") {
		t.Fatal("unknown tier should be rejected")
	}
	if treat(g, "zzz", "preventive") {
		t.Fatal("unknown hex should be rejected")
	}
}

// TestTierCostRankBreaksTiesStably guards the equal-cost regression: two tiers
// sharing a CostPerSqM must still get distinct ranks (0..n-1), so the priciest
// tier reaches frac==1.0 and restores PCI to 100. Before the stable tiebreak,
// "mid" and "top" below both counted one strictly-cheaper tier -> rank 1 -> the
// top tier never restored to 100.
func TestTierCostRankBreaksTiesStably(t *testing.T) {
	cfg := baseConfig()
	cfg.CostTiers = []forecast.CostTier{
		{MinPCI: 70, MaxPCI: 101, CostPerSqM: 5.00, Label: "cheap"},
		{MinPCI: 40, MaxPCI: 70, CostPerSqM: 150.00, Label: "mid"},
		{MinPCI: 0, MaxPCI: 40, CostPerSqM: 150.00, Label: "top"}, // duplicate cost
	}
	g := newGame(t, cfg)

	// (a) ranks are a permutation of 0..n-1 (unique, no collisions).
	seen := make(map[int]bool, len(g.tiers))
	for i := range g.tiers {
		r := g.tierRanks[i]
		if r < 0 || r >= len(g.tiers) {
			t.Fatalf("rank out of range for tier %d: %d", i, r)
		}
		if seen[r] {
			t.Fatalf("duplicate rank %d — tiebreak failed", r)
		}
		seen[r] = true
	}

	// (b) the highest-index priciest tier ("top") restores a gravel hex to ~100.
	g.hexes[0].pci = 0
	g.hexes[0].closed = true
	if !treat(g, "a", "top") {
		t.Fatal("top tier should be affordable")
	}
	if g.hexes[0].pci < 99.999 {
		t.Fatalf("priciest tier must restore ~100, got %v", g.hexes[0].pci)
	}

	// A same-cost lower-index tier ("mid") gets a smaller frac, not a full restore.
	g.hexes[1].pci = 0
	if !treat(g, "b", "mid") {
		t.Fatal("mid tier should be affordable")
	}
	if g.hexes[1].pci >= 99.999 {
		t.Fatalf("mid tier should lift partially, not to 100, got %v", g.hexes[1].pci)
	}
}

func TestClosureAtZero(t *testing.T) {
	cfg := baseConfig()
	cfg.Hexes = []HexConfig{{ID: "a", RoadsArea: 1000, DecayRate: 5.0}} // very fast decay
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

// TestProjectInsolvency asserts the board-free pregame projection moves the
// right way with budget: a starving budget is insolvent within the horizon, an
// ample budget is solvent, and it needs no hexes (pregame has cohorts but no
// board yet).
func TestProjectInsolvency(t *testing.T) {
	cfg := baseConfig()

	cfg.AnnualBudget = 0
	year, ok := ProjectInsolvency(cfg)
	if !ok {
		t.Fatal("expected insolvency within horizon at budget 0")
	}
	if year <= 0 || year > int(cfg.HorizonYears) {
		t.Fatalf("insolvency year %d out of range (1..%d)", year, int(cfg.HorizonYears))
	}

	cfg.AnnualBudget = 1e12
	if _, ok := ProjectInsolvency(cfg); ok {
		t.Fatal("expected solvent-through-horizon at a huge budget")
	}

	// No board required: cohorts drive the projection, hexes are ignored.
	cfg.AnnualBudget = 0
	cfg.Hexes = nil
	if _, ok := ProjectInsolvency(cfg); !ok {
		t.Fatal("expected insolvency at budget 0 even with no hexes")
	}
}

// TestProjectInsolvencyBadHorizon asserts an out-of-range horizon returns the
// "no result" signal (ok=false) without panicking through
// forecast.Simulate -> EstimateGrowth's make([]float64, years).
func TestProjectInsolvencyBadHorizon(t *testing.T) {
	for name, h := range map[string]float64{
		"negative": -10,
		"zero":     0,
		"huge":     1e18,
	} {
		t.Run(name, func(t *testing.T) {
			cfg := baseConfig()
			cfg.HorizonYears = h
			if _, ok := ProjectInsolvency(cfg); ok {
				t.Fatalf("expected ok=false for horizon %g", h)
			}
		})
	}
}

// TestProjectInsolvencyNonFiniteInputs asserts the pregame preview path (which
// reaches insolvencyFromForecast straight from an untrusted Config, bypassing
// New) treats non-finite/out-of-range numerics as "no result" (ok=false) rather
// than returning a silently garbage year.
func TestProjectInsolvencyNonFiniteInputs(t *testing.T) {
	cases := map[string]func(*Config){
		"nan initial_pci":  func(c *Config) { c.InitialPCI = math.NaN() },
		"initial_pci>100":  func(c *Config) { c.InitialPCI = 101 },
		"inf growth":       func(c *Config) { c.GrowthRate = math.Inf(1) },
		"nan budget":       func(c *Config) { c.AnnualBudget = math.NaN() },
		"nan tier cost":    func(c *Config) { c.CostTiers[0].CostPerSqM = math.NaN() },
		"neg cohort area":  func(c *Config) { c.Cohorts[0].Area = -1 },
		"inf cohort decay": func(c *Config) { c.Cohorts[0].DecayRate = math.Inf(1) },
		// A sub-annual cycle survives ResolveCycleYears and makes
		// eligibleFrac = 1/1e-9 = 1e9, which reported insolvency in year 1 even
		// at a $1e12 budget before the re-guard.
		"tiny cycle": func(c *Config) { c.TreatmentCycleYears = 1e-9 },
		"huge cycle": func(c *Config) { c.TreatmentCycleYears = 41 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := baseConfig()
			mutate(&cfg)
			if _, ok := ProjectInsolvency(cfg); ok {
				t.Fatalf("expected ok=false for %q", name)
			}
		})
	}
}

// TestProjectInsolvencyTinyCycleIgnoresBudget pins the specific symptom the
// cycleYears re-guard closes: before it, a sub-annual treatment cycle reported
// insolvency in year 1 no matter how large the budget was, because
// eligibleFrac = 1/cycleYears made the entire network eligible every year.
func TestProjectInsolvencyTinyCycleIgnoresBudget(t *testing.T) {
	cfg := baseConfig()
	cfg.TreatmentCycleYears = 1e-9
	cfg.AnnualBudget = 1e12
	if year, ok := ProjectInsolvency(cfg); ok {
		t.Fatalf("expected ok=false for a sub-annual cycle at a $1e12 budget, got year %d", year)
	}
}

// TestNewNegativeBudgetClamped: a negative AnnualBudget must seed
// treasury/annualBudget at >= 0 (no debt model).
func TestNewNegativeBudgetClamped(t *testing.T) {
	cfg := baseConfig()
	cfg.AnnualBudget = -1_000_000
	g := newGame(t, cfg)
	if g.annualBudget < 0 {
		t.Errorf("annualBudget should clamp to >= 0, got %v", g.annualBudget)
	}
	if g.treasury < 0 {
		t.Errorf("treasury should clamp to >= 0, got %v", g.treasury)
	}
}

func TestWinTransition(t *testing.T) {
	cfg := baseConfig()
	cfg.HorizonYears = 2
	cfg.Hexes = []HexConfig{{ID: "a", RoadsArea: 1000, DecayRate: 0.0001}} // barely decays
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
		{ID: "a", RoadsArea: 1000, DecayRate: 1.0},
		{ID: "b", RoadsArea: 1000, DecayRate: 1.0},
	}
	cfg.InitialPCI = 40
	cfg.AnnualBudget = 0
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
	for _, k := range []string{"year", "treasury", "annual_budget", "network_pci", "deferred_backlog", "status", "total_spend", "treatment_count", "changed"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("StateJSON missing key %q", k)
		}
	}
	// The pregame projection owns the insolvency year, and the front-end
	// colours hexes by band, so neither is in the live state.
	if _, ok := m["insolvency_year"]; ok {
		t.Error("StateJSON carries insolvency_year")
	}
	var changed []map[string]json.RawMessage
	if err := json.Unmarshal(m["changed"], &changed); err != nil {
		t.Fatal(err)
	}
	if len(changed) == 0 {
		t.Fatal("first StateJSON carries no changed hexes")
	}
	if _, ok := changed[0]["pci"]; ok {
		t.Error("changed hexes carry pci")
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
	Year         float64      `json:"year"`
	Treasury     float64      `json:"treasury"`
	AnnualBudget float64      `json:"annual_budget"`
	NetworkPCI   float64      `json:"network_pci"`
	Backlog      float64      `json:"deferred_backlog"`
	Status       string       `json:"status"`
	LossReason   string       `json:"loss_reason"`
	OutOfFunds   bool         `json:"out_of_funds"`
	Spent        float64      `json:"total_spend"`
	Treatments   int          `json:"treatment_count"`
	ClosedShare  float64      `json:"closed_share"`
	Changed      []changedHex `json:"changed"`
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
	if !treat(g, "a", "auto") {
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
	cfg.Hexes = []HexConfig{{ID: "a", RoadsArea: 1000, DecayRate: 0.0001}} // barely decays
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
	treat(g, "a", "preventive") // 5 * 1000
	treat(g, "b", "preventive") // 5 * 2000
	if g.treatments != 2 || math.Abs(g.spent-15000) > 1e-6 {
		t.Fatalf("after 2 treats: spent=%v treatments=%d, want 15000/2", g.spent, g.treatments)
	}
	g.treasury = 0 // make the next treat unaffordable
	if treat(g, "a", "reconstruction") {
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
// totals (spent/treatments) and the live closed_share.
func TestStateJSONSummaryFields(t *testing.T) {
	g := newGame(t, baseConfig())
	g.hexes[0].closed = true // 1000 of 3000 m^2 closed (under the 50% loss threshold)
	treat(g, "b", "preventive")
	st := decodeState(t, g)
	if math.Abs(st.ClosedShare-1.0/3) > 1e-12 {
		t.Fatalf("closed_share = %v, want 1/3 of the road area", st.ClosedShare)
	}
	if st.Treatments != 1 || st.Spent <= 0 {
		t.Fatalf("summary fields wrong: spent=%v treatments=%d", st.Spent, st.Treatments)
	}
}

// idleConfig is a board that decays slowly enough that neither the PCI floor
// nor closures end the game inside the horizon, so only insolvency can.
func idleConfig(budget float64) Config {
	cfg := baseConfig()
	cfg.HorizonYears = 20
	cfg.InitialPCI = 75
	cfg.AnnualBudget = budget
	cfg.Hexes = []HexConfig{
		{ID: "a", RoadsArea: 1000, DecayRate: 0.02},
		{ID: "b", RoadsArea: 2000, DecayRate: 0.02},
	}
	cfg.Cohorts = []CohortConfig{{Classification: "residential", Area: 3000, DecayRate: 0.02}}
	return cfg
}

// playIdle ticks a year at a time without treating anything and returns the
// year the game ended in (0 if it did not end).
func playIdle(g *Game) int {
	for y := 1; y <= 30; y++ {
		g.Tick(1)
		if g.status != "running" {
			return y
		}
	}
	return 0
}

// TestIdlePlayerGoesInsolvent: an idle player used
// to win "You stayed solvent!" while the start screen predicted insolvency,
// because the loss ceiling was the cost of rebuilding the whole network.
// Insolvency is now the forecast's definition: a full treatment cycle of
// year-1 need deferred. Doing nothing defers a growing need every year (worse
// pavement costs more), so it gets there before the 12-year cycle ends.
func TestIdlePlayerGoesInsolvent(t *testing.T) {
	g := newGame(t, idleConfig(1_000_000))
	year := playIdle(g)
	if g.status != "lost" {
		t.Fatalf("idle player finished %q, want lost to insolvency", g.status)
	}
	if g.networkPCI() < LosePCIFloor {
		t.Fatalf("fixture lost on the PCI floor (%.1f), not insolvency", g.networkPCI())
	}
	if year < 6 || year > 12 {
		t.Errorf("idle player went insolvent in year %d, want within the 12-year cycle", year)
	}
}

// TestLossReason: a lost game names the
// condition that ended it, so the banner can tell an idle player with a full
// treasury that the money went unspent rather than that decay outran it.
func TestLossReason(t *testing.T) {
	t.Run("running and won report none", func(t *testing.T) {
		cfg := baseConfig()
		cfg.HorizonYears = 1
		cfg.InitialPCI = 95
		cfg.Cohorts = nil
		g := newGame(t, cfg)
		if st := decodeState(t, g); st.LossReason != "" {
			t.Errorf("running game loss_reason = %q, want empty", st.LossReason)
		}
		g.Tick(1.5)
		st := decodeState(t, g)
		if st.Status != "won" || st.LossReason != "" {
			t.Errorf("won game: status %q loss_reason %q, want won and empty", st.Status, st.LossReason)
		}
	})
	t.Run("backlog", func(t *testing.T) {
		g := newGame(t, idleConfig(1_000_000))
		playIdle(g)
		st := decodeState(t, g)
		if st.Status != "lost" || st.LossReason != LossBacklog {
			t.Errorf("idle player: status %q loss_reason %q, want lost to %q", st.Status, st.LossReason, LossBacklog)
		}
		if st.Treasury <= 0 || st.Treatments != 0 {
			t.Errorf("fixture should lose with money unspent: treasury %v, treatments %d", st.Treasury, st.Treatments)
		}
	})
	t.Run("pci floor", func(t *testing.T) {
		cfg := baseConfig()
		cfg.HorizonYears = 100
		cfg.Hexes = []HexConfig{{ID: "a", RoadsArea: 1000, DecayRate: 0.05}, {ID: "b", RoadsArea: 1000, DecayRate: 0.05}}
		cfg.InitialPCI = 32
		cfg.AnnualBudget = 0
		cfg.Cohorts = nil
		g := newGame(t, cfg)
		for i := 0; i < 100 && g.status == "running"; i++ {
			g.Tick(0.1)
		}
		if st := decodeState(t, g); st.LossReason != LossPCIFloor {
			t.Errorf("loss_reason = %q (status %q), want %q", st.LossReason, st.Status, LossPCIFloor)
		}
	})
	t.Run("closures", func(t *testing.T) {
		cfg := baseConfig()
		cfg.HorizonYears = 100
		// Two closed hexes holding most of the road area, and one pristine
		// hex holding the open network's PCI above the floor.
		cfg.Hexes = []HexConfig{
			{ID: "a", RoadsArea: 600, DecayRate: 0.01},
			{ID: "b", RoadsArea: 600, DecayRate: 0.01},
			{ID: "c", RoadsArea: 1000, DecayRate: 0.01},
		}
		cfg.InitialPCI = 95
		cfg.AnnualBudget = 0
		cfg.Cohorts = nil
		g := newGame(t, cfg)
		g.hexByID["a"].pci, g.hexByID["a"].closed = 0, true
		g.hexByID["b"].pci, g.hexByID["b"].closed = 0, true
		g.Tick(0.01)
		if st := decodeState(t, g); st.LossReason != LossClosures {
			t.Errorf("loss_reason = %q (status %q), want %q", st.LossReason, st.Status, LossClosures)
		}
	})
}

// TestLiveInsolvencyMatchesProjection: at a $0 budget the pregame projection
// and the live game, played idle at the frame-sized ticks the browser uses,
// report insolvency in the same year, for slow and fast decay alike.
// Whole-year ticks price each year's need at its
// end-of-year condition only and can land a year early, which an earlier
// +-1 tolerance hid; the browser never ticks that coarsely.
func TestLiveInsolvencyMatchesProjection(t *testing.T) {
	const dt = 1.0 / 60
	for _, k := range []float64{0.02, 0.04, 0.06, 0.09} {
		cfg := idleConfig(0)
		for i := range cfg.Hexes {
			cfg.Hexes[i].DecayRate = k
		}
		cfg.Cohorts[0].DecayRate = k
		want, ok := ProjectInsolvency(cfg)
		if !ok {
			t.Fatalf("k=%g: projection says solvent at $0", k)
		}
		g := newGame(t, cfg)
		for step := 0; step < 40*60 && g.status == "running"; step++ {
			g.Tick(dt)
		}
		if g.status != "lost" {
			t.Fatalf("k=%g: idle game finished %q, want lost", k, g.status)
		}
		if g.yearsDone != want {
			t.Errorf("k=%g: live game went insolvent in year %d, projection says %d", k, g.yearsDone, want)
		}
	}
}

// TestFundedYearDefersNothing: treatment spend that covers the year's need
// leaves nothing deferred.
func TestFundedYearDefersNothing(t *testing.T) {
	g := newGame(t, idleConfig(1_000_000))
	g.Tick(0.5)
	for _, id := range []string{"a", "b"} {
		if !treat(g, id, "auto") {
			t.Fatalf("treat %s failed", id)
		}
	}
	g.Tick(0.5)
	if g.backlog != 0 {
		t.Errorf("backlog after a funded year = %v, want 0", g.backlog)
	}
	g.Tick(1)
	if g.backlog <= 0 {
		t.Error("an unfunded year deferred nothing")
	}
}

// Where the cost tiers leave a PCI gap, the "auto" brush applies the tier the
// forecast prices that pavement at (the nearest band) rather than nothing.
func TestAutoTierSelection_GapUsesTheForecastsTier(t *testing.T) {
	cfg := baseConfig()
	cfg.CostTiers = []forecast.CostTier{
		{MinPCI: 80, MaxPCI: 101, CostPerSqM: 5, Label: "seal"},
		{MinPCI: 0, MaxPCI: 40, CostPerSqM: 150, Label: "rebuild"},
	}
	g := newGame(t, cfg)
	for pci, want := range map[float64]string{70: "seal", 50: "rebuild", 90: "seal", 10: "rebuild"} {
		i := g.autoTierIndex(pci)
		if i < 0 || g.tiers[i].Label != want {
			t.Errorf("autoTierIndex(%g) = %d, want %s", pci, i, want)
		}
		if fi := forecast.TierIndexForPCI(cfg.CostTiers, pci); fi != i {
			t.Errorf("game picks tier %d at PCI %g, the forecast tier %d", i, pci, fi)
		}
	}
}

// TestForecastBoundsHoldForNewAndTheProjection pins the game's two entry
// points to one set of forecast bounds: an input New refuses is one the
// pregame projection (which never goes through New) reports no result for.
func TestForecastBoundsHoldForNewAndTheProjection(t *testing.T) {
	unfunded := func() Config {
		cfg := baseConfig()
		cfg.AnnualBudget = 0
		return cfg
	}
	if _, err := New(unfunded()); err != nil {
		t.Fatalf("New(valid): %v", err)
	}
	if _, ok := ProjectInsolvency(unfunded()); !ok {
		t.Fatal("an unfunded network should project an insolvency year")
	}

	cases := map[string]func(*Config){
		"horizon over the maximum": func(c *Config) { c.HorizonYears = forecast.MaxHorizonYears + 1 },
		"horizon under a year":     func(c *Config) { c.HorizonYears = 0.5 },
		"initial pci over 100":     func(c *Config) { c.InitialPCI = 101 },
		"growth over 0.5":          func(c *Config) { c.GrowthRate = 0.6 },
		"negative cycle":           func(c *Config) { c.TreatmentCycleYears = -1 },
		"cycle over 40":            func(c *Config) { c.TreatmentCycleYears = 41 },
		"free tier":                func(c *Config) { c.CostTiers[0].CostPerSqM = 0 },
		"inverted tier band":       func(c *Config) { c.CostTiers[0].MinPCI, c.CostTiers[0].MaxPCI = 90, 80 },
		"unlabelled tier":          func(c *Config) { c.CostTiers[0].Label = "" },
		"negative cohort area":     func(c *Config) { c.Cohorts[0].Area = -1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := unfunded()
			mutate(&cfg)
			if _, err := New(cfg); err == nil {
				t.Error("New accepted it")
			}
			if year, ok := ProjectInsolvency(cfg); ok {
				t.Errorf("ProjectInsolvency projected year %d", year)
			}
		})
	}

	// The longest horizon a config may state starts a game and projects.
	cfg := unfunded()
	cfg.HorizonYears = forecast.MaxHorizonYears
	if _, err := New(cfg); err != nil {
		t.Errorf("New at the maximum horizon: %v", err)
	}
	if _, ok := ProjectInsolvency(cfg); !ok {
		t.Error("no projection at the maximum horizon")
	}
}

// TestCostMultiplierLoadsEveryCost: the multiplier scales what a treatment debits,
// the board's need and the macro forecast together, so a loaded budget meets
// loaded costs. Dollars are linear in both, so the projection at a multiplier
// and a budget is the bare projection at the budget over the multiplier.
func TestCostMultiplierLoadsEveryCost(t *testing.T) {
	const multiplier = 1.5
	bare := newGame(t, baseConfig())
	cfg := baseConfig()
	cfg.CostMultiplier = multiplier
	loaded := newGame(t, cfg)

	before := loaded.treasury
	if !treat(loaded, "a", "preventive") {
		t.Fatal("preventive should be affordable")
	}
	if got, want := before-loaded.treasury, multiplier*5*1000; math.Abs(got-want) > 1e-6 {
		t.Errorf("loaded debit = %v, want %v", got, want)
	}
	if got, want := loaded.insolvencyAt, multiplier*bare.insolvencyAt; math.Abs(got-want) > 1e-6*want {
		t.Errorf("loaded insolvency threshold = %v, want %v", got, want)
	}

	for _, budget := range []float64{0, 50_000, 200_000} {
		cfg.AnnualBudget = budget * multiplier
		gotYear, gotOK := ProjectInsolvency(cfg)
		bareCfg := baseConfig()
		bareCfg.AnnualBudget = budget
		wantYear, wantOK := ProjectInsolvency(bareCfg)
		if gotYear != wantYear || gotOK != wantOK {
			t.Errorf("budget %g: loaded projection (%d,%v), bare at budget/multiplier (%d,%v)",
				budget, gotYear, gotOK, wantYear, wantOK)
		}
	}
}

// TestOutOfFunds: the warning fires when nothing the brush would treat is
// affordable. A sliver hex the map rounds to a fraction of a square metre does
// not hold it off, and each hex is priced at the tier its condition calls for.
func TestOutOfFunds(t *testing.T) {
	cfg := baseConfig()
	cfg.Hexes = []HexConfig{
		{ID: "a", RoadsArea: 1000, DecayRate: 0.02},
		{ID: "b", RoadsArea: 2000, DecayRate: 0.02},
		{ID: "sliver", RoadsArea: 0.01, DecayRate: 0.02},
	}
	g := newGame(t, cfg) // PCI 80: preventive at $5/m^2, $5,000 for "a"

	g.treasury = 4_000
	if !decodeState(t, g).OutOfFunds {
		t.Error("$4,000 treats no road hex, but a sliver held the warning off")
	}
	g.treasury = 6_000
	if decodeState(t, g).OutOfFunds {
		t.Error("$6,000 treats hex a, but the warning is up")
	}
	// At PCI 20 the brush applies reconstruction ($150/m^2), so $6,000 buys
	// nothing, though it would cover preventive work.
	for i := range g.hexes {
		g.hexes[i].pci = 20
	}
	if !decodeState(t, g).OutOfFunds {
		t.Error("$6,000 cannot reconstruct any hex, but the warning is down")
	}

	// A board of nothing but slivers is judged on them.
	cfg.Hexes = []HexConfig{{ID: "s", RoadsArea: 0.01, DecayRate: 0.02}}
	g = newGame(t, cfg)
	g.treasury = 1
	if decodeState(t, g).OutOfFunds {
		t.Error("$1 treats the only hex, but the warning is up")
	}
}

// TestOutOfFundsIgnoresZeroAreaHexes: hexes the map rounds to 0 m² cost
// nothing to treat, so they neither hold the warning off nor, when they are
// most of the board, drag the sliver threshold to zero.
func TestOutOfFundsIgnoresZeroAreaHexes(t *testing.T) {
	cfg := baseConfig()
	cfg.Hexes = []HexConfig{
		{ID: "z1", RoadsArea: 0, DecayRate: 0.02},
		{ID: "z2", RoadsArea: 0, DecayRate: 0.02},
		{ID: "z3", RoadsArea: 0, DecayRate: 0.02},
		{ID: "sliver", RoadsArea: 0.01, DecayRate: 0.02},
		{ID: "a", RoadsArea: 1000, DecayRate: 0.02},
	}
	g := newGame(t, cfg) // PCI 80: preventive at $5/m^2, $5,000 for "a"
	if got, want := g.sliverArea, SliverAreaShare*500.005; math.Abs(got-want) > 1e-9 {
		t.Errorf("sliver threshold = %v, want %v (the median of the hexes holding road)", got, want)
	}
	g.treasury = 4_000
	if !decodeState(t, g).OutOfFunds {
		t.Error("$4,000 treats no road hex, but zero-area hexes held the warning off")
	}
	g.treasury = 6_000
	if decodeState(t, g).OutOfFunds {
		t.Error("$6,000 treats hex a, but the warning is up")
	}
}

// TestClosuresWeighRoadArea: the closures loss is a share of road area, so
// closing most hexes when they are slivers loses nothing, while closing one
// hex that holds most of the road does.
func TestClosuresWeighRoadArea(t *testing.T) {
	cfg := baseConfig()
	cfg.HorizonYears = 100
	cfg.InitialPCI = 95
	cfg.AnnualBudget = 0
	cfg.Cohorts = nil
	cfg.Hexes = []HexConfig{
		{ID: "s1", RoadsArea: 1, DecayRate: 0.01},
		{ID: "s2", RoadsArea: 1, DecayRate: 0.01},
		{ID: "s3", RoadsArea: 1, DecayRate: 0.01},
		{ID: "big", RoadsArea: 1000, DecayRate: 0.01},
	}
	g := newGame(t, cfg)
	for _, id := range []string{"s1", "s2", "s3"} {
		g.hexByID[id].pci, g.hexByID[id].closed = 0, true
	}
	g.Tick(0.01)
	if g.status != statusRunning {
		t.Fatalf("closing three slivers ended the game: %q (%s)", g.status, g.lossReason)
	}

	g = newGame(t, cfg)
	g.hexByID["big"].pci, g.hexByID["big"].closed = 0, true
	g.Tick(0.01)
	if g.lossReason != LossClosures {
		t.Fatalf("closing the hex holding most of the road: status %q loss_reason %q, want %q", g.status, g.lossReason, LossClosures)
	}
}

// TestPCIJitterDefault: a config that leaves pci_jitter out gets
// DefaultPCIJitter, and an explicit 0 starts the board flat.
func TestPCIJitterDefault(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.pciJitter(); got != DefaultPCIJitter {
		t.Errorf("omitted pci_jitter = %v, want %v", got, DefaultPCIJitter)
	}

	cfg = baseConfig()
	cfg.PCIJitter = nil
	g := newGame(t, cfg)
	spread := false
	for _, h := range g.hexes {
		d := math.Abs(h.pci - cfg.InitialPCI)
		if d > DefaultPCIJitter+1e-9 {
			t.Errorf("hex %s starts %v from the initial PCI, beyond the default jitter", h.id, d)
		}
		spread = spread || d > 0
	}
	if !spread {
		t.Error("the default jitter left every hex at the initial PCI")
	}

	cfg.PCIJitter = new(0.0)
	for _, h := range newGame(t, cfg).hexes {
		if h.pci != cfg.InitialPCI {
			t.Errorf("hex %s starts at %v with zero jitter, want %v", h.id, h.pci, cfg.InitialPCI)
		}
	}
}
