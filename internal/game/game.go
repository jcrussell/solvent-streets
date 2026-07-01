// Package game holds all state and rules for the Solvent Streets real-time
// pavement-management RTS. It is pure Go and build-tag-free so it can be unit
// tested on the host; a thin syscall/js shim (built separately) drives it from
// the browser, mirroring the internal/forecast/bridge + cmd/wasm/forecast split.
//
// Fidelity note: the PCI decay form (PCI = PCI0*exp(-k*t)), the per-class decay
// rates, the tiered $/m^2 cost curve, and the macro "insolvent in N years"
// headline are the *real* forecast math (internal/forecast). The per-hex
// treatment dynamics (click a hex, spend to lift its PCI to a tier target, hexes
// closing to gravel at PCI 0) are a deliberately simplified *game model* inspired
// by — but not a faithful port of — forecast.applyCohortSpend. Do not advertise
// engine fidelity it lacks.
package game

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math"

	"github.com/jcrussell/solvent-streets/internal/forecast"
)

// --- Tunable game constants (exported so the design is inspectable/testable) ---

const (
	// BandCount is the number of quantized PCI color bands. The front-end and Go
	// agree on this so a hex only repaints when it crosses a band edge.
	BandCount = 6

	// ClosureEpsilon is the PCI at or below which an open hex flips to "closed"
	// (reverts to gravel). A treatment that lifts a hex back above this reopens it.
	ClosureEpsilon = 1.0

	// LosePCIFloor: if the area-weighted network PCI (over open hexes) drops below
	// this, the network has collapsed and the game is lost.
	LosePCIFloor = 30.0

	// LoseClosedFraction: if more than this fraction of hexes have closed, the
	// game is lost.
	LoseClosedFraction = 0.5
)

// --- Config: the JSON contract decoded from the browser at gameInit ---

// HexConfig is a single board hex: a stable id, its road footprint in m^2, and
// its area-blended decay rate k (per year).
type HexConfig struct {
	ID       string  `json:"id"`
	RoadArea float64 `json:"road_area"`
	K        float64 `json:"k"`
}

// CohortConfig mirrors a per-classification cohort for the macro forecast.
// Equivalent to forecast.Cohort minus initial_pci (which is supplied globally).
type CohortConfig struct {
	Classification string  `json:"classification"`
	Area           float64 `json:"area"`
	DecayRate      float64 `json:"decay_rate"`
}

// Config is the full game setup decoded from JSON at gameInit. It is the
// untrusted JS->WASM boundary, so New validates it before trusting it.
type Config struct {
	Hexes []HexConfig `json:"hexes"`

	InitialPCI float64 `json:"initial_pci"`
	// PCIJitter spreads the starting PCI deterministically per hex (see New) so
	// the board is not a flat color while staying reproducible across runs/tests.
	PCIJitter float64 `json:"pci_jitter"`

	CostTiers []forecast.CostTier `json:"cost_tiers"`

	// StartingBudget is the annual funding rate ($/sim-year) that refills the
	// treasury and drives the macro insolvency headline.
	StartingBudget float64 `json:"starting_budget"`

	HorizonYears float64 `json:"horizon_years"`

	// TreatmentCycleYears is N for the macro forecast (annual need = full-network
	// cost / N). 0 resolves to forecast.DefaultTreatmentCycleYears.
	TreatmentCycleYears float64 `json:"treatment_cycle_years"`

	GrowthRate float64 `json:"growth_rate"`

	// Cohorts feed the macro forecast.Simulate call for the insolvency headline.
	Cohorts []CohortConfig `json:"cohorts"`

	// Endless suppresses the time-based win: the game never auto-"wins" at the
	// horizon, so play continues until a loss condition triggers (sandbox mode).
	// HorizonYears must still be > 0 — it is used only for the insolvency
	// projection now, not as a finish line. A huge HorizonYears must NOT be used
	// to fake endless: recomputeInsolvency runs a per-year forecast over it.
	Endless bool `json:"endless"`
}

// --- Game state ---

type hexState struct {
	id       string
	roadArea float64
	k        float64
	pci      float64
	closed   bool

	// paint state for the StateJSON delta: the band/closed last emitted to the
	// front-end. paintedBand == -1 means "never painted" (force first emit).
	paintedBand   int
	paintedClosed bool
}

// Game holds all sim state. Returned as a concrete *Game (byob-interfaces.2).
type Game struct {
	hexes []hexState

	treasury   float64
	budgetRate float64
	backlog    float64 // cumulative maintenance need not yet addressed by treatment
	simYears   float64
	horizon    float64
	endless    bool   // when true, never auto-win at the horizon (sandbox)
	status     string // "running" | "won" | "lost"

	// run totals for the end-of-game summary
	spent      float64 // cumulative $ debited by treatments
	treatments int     // count of successful treatments

	cost       *forecast.TieredCostProjector
	tiers      []forecast.CostTier
	initialPCI float64

	// minTierCost is the cheapest tier's $/m^2. Combined with the smallest open
	// hex's area it gives a lower bound on any single treatment's cost, which is
	// the "can't afford anything" (out-of-funds) test in StateJSON.
	minTierCost float64

	// macro-forecast inputs (for SetBudget's insolvency recompute)
	cohorts    []CohortConfig
	growthRate float64
	cycleYears float64

	// cached macro insolvency headline
	insolvencyYear int
	insolvencyOK   bool

	// loseBacklogCeiling is the backlog at which the game is lost: the cost to
	// reconstruct the entire network (total road area x the priciest tier). Once
	// you have deferred a whole network's worth of work, you have spiraled.
	loseBacklogCeiling float64
}

// New validates cfg and builds a Game. It returns error (not a concrete type)
// per byob-errors.5. Validation (byob-input-validation.2) rejects empty hexes,
// non-positive k, negative road_area, non-positive horizon, and empty cost_tiers.
//
// Starting PCI per hex is initial_pci offset by a *deterministic* jitter derived
// from an FNV hash of the hex id (range [-pci_jitter, +pci_jitter], clamped to
// [0,100]). No global math/rand is used, so runs and tests are reproducible.
func New(cfg Config) (*Game, error) {
	if len(cfg.Hexes) == 0 {
		return nil, errors.New("game: config has no hexes")
	}
	if cfg.HorizonYears <= 0 {
		return nil, fmt.Errorf("game: horizon_years must be > 0, got %g", cfg.HorizonYears)
	}
	if len(cfg.CostTiers) == 0 {
		return nil, errors.New("game: cost_tiers must be non-empty")
	}
	for i, h := range cfg.Hexes {
		if h.ID == "" {
			return nil, fmt.Errorf("game: hex %d has empty id", i)
		}
		if h.K <= 0 {
			return nil, fmt.Errorf("game: hex %q has non-positive k %g", h.ID, h.K)
		}
		if h.RoadArea < 0 {
			return nil, fmt.Errorf("game: hex %q has negative road_area %g", h.ID, h.RoadArea)
		}
	}

	tiers := make([]forecast.CostTier, len(cfg.CostTiers))
	copy(tiers, cfg.CostTiers)

	g := &Game{
		treasury:   cfg.StartingBudget,
		budgetRate: cfg.StartingBudget,
		horizon:    cfg.HorizonYears,
		endless:    cfg.Endless,
		status:     "running",
		cost:       &forecast.TieredCostProjector{Tiers: tiers},
		tiers:      tiers,
		initialPCI: cfg.InitialPCI,
		cohorts:    cfg.Cohorts,
		growthRate: cfg.GrowthRate,
		cycleYears: forecast.ResolveCycleYears(cfg.TreatmentCycleYears),
	}

	maxCost := 0.0
	minCost := math.Inf(1)
	for _, t := range tiers {
		if t.CostPerSqM > maxCost {
			maxCost = t.CostPerSqM
		}
		if t.CostPerSqM < minCost {
			minCost = t.CostPerSqM
		}
	}
	g.minTierCost = minCost

	g.hexes = make([]hexState, len(cfg.Hexes))
	var totalArea float64
	for i, h := range cfg.Hexes {
		pci := clampPCI(cfg.InitialPCI + jitterOffset(h.ID, cfg.PCIJitter))
		g.hexes[i] = hexState{
			id:          h.ID,
			roadArea:    h.RoadArea,
			k:           h.K,
			pci:         pci,
			closed:      pci <= ClosureEpsilon,
			paintedBand: -1, // never painted -> first StateJSON emits all hexes
		}
		totalArea += h.RoadArea
	}
	g.loseBacklogCeiling = totalArea * maxCost

	// Initial macro insolvency headline from the real engine.
	g.recomputeInsolvency()
	return g, nil
}

// Tick advances sim time by dt sim-years. Each open hex decays continuously by
// exp(-k*dt); because exp composes (prod over a year of exp(-k*dt) == exp(-k*1)),
// repeated ticks land exactly on the annual ExponentialPCIForecaster step with no
// drift. The treasury refills at budgetRate*dt. This tick's maintenance need —
// the tiered cost of the PCI lost, scaled by the fraction of full condition lost
// (delta/100) — accrues into the backlog. Hexes at or below ClosureEpsilon close.
func (g *Game) Tick(dt float64) {
	// Reject non-finite or non-positive dt: a bare dt<=0 guard lets NaN and +Inf
	// through (NaN<=0 and +Inf<=0 are both false), which would poison simYears,
	// treasury, and every hex's PCI with NaN irrecoverably.
	if !finite(dt) || dt <= 0 || g.status != "running" {
		return
	}
	g.simYears += dt
	g.treasury += g.budgetRate * dt

	for i := range g.hexes {
		h := &g.hexes[i]
		if h.closed {
			continue
		}
		before := h.pci
		after := before * math.Exp(-h.k*dt)
		delta := before - after
		if delta > 0 {
			// Full treatment cost at the post-decay condition, weighted by the
			// share of full condition (100 PCI points) lost this tick.
			g.backlog += g.cost.ProjectCost(h.roadArea, after) * (delta / 100.0)
		}
		h.pci = after
		if h.pci <= ClosureEpsilon {
			h.pci = 0
			h.closed = true
		}
	}

	g.recomputeStatus()
}

// Treat applies a treatment of the given tier (by label, as found in cost_tiers)
// to one hex. If the treasury cannot afford tier.CostPerSqM * road_area, it is a
// no-op and returns false. Otherwise it debits the treasury, draws the spend down
// against the backlog, and raises the hex's PCI toward 100 by a fraction set by
// the tier's cost rank: the priciest tier (reconstruction) restores to 100, the
// cheapest (preventive) gives the smallest lift. Any treatment that lifts a closed
// hex back above ClosureEpsilon reopens it (reconstruction always does).
func (g *Game) Treat(hexID, tier string) bool {
	applied := g.treatOne(hexID, tier)
	if applied {
		g.recomputeStatus()
	}
	return applied
}

// TreatBatch treats many hexes with one status recompute, for the paint brush: a
// single drag-move can cover dozens of hexes, and recomputing status (O(hexes))
// per hex would be quadratic. It applies treatOne to each id (each affordable hex
// is treated; the rest are skipped silently, e.g. once the treasury runs dry) and
// recomputes status once at the end. Returns how many were applied.
func (g *Game) TreatBatch(hexIDs []string, tier string) int {
	applied := 0
	for _, id := range hexIDs {
		if g.treatOne(id, tier) {
			applied++
		}
	}
	if applied > 0 {
		g.recomputeStatus()
	}
	return applied
}

// treatOne applies a single treatment WITHOUT recomputing status (the caller
// does, once). tier is a cost_tiers label, or the sentinel "auto" to let the
// engine pick the tier whose [MinPCI, MaxPCI) band contains the hex's current
// PCI — so the front-end never has to choose preventive/rehab/reconstruction per
// hex. Returns false (no-op) if the game is over, the tier/hex is unknown, or the
// treasury can't afford it. Accumulates spent/treatments for the summary.
func (g *Game) treatOne(hexID, tier string) bool {
	if g.status != "running" {
		return false
	}
	h := g.hex(hexID)
	if h == nil {
		return false
	}
	ti := g.resolveTier(tier, h.pci)
	if ti < 0 {
		return false
	}

	cost := g.tiers[ti].CostPerSqM * h.roadArea
	if g.treasury < cost {
		return false
	}
	g.treasury -= cost
	g.spent += cost
	g.treatments++

	// Spending addresses deferred need.
	g.backlog -= cost
	if g.backlog < 0 {
		g.backlog = 0
	}

	// Lift toward 100 by (rank+1)/n of the remaining headroom; top tier -> 100.
	frac := float64(g.tierCostRank(ti)+1) / float64(len(g.tiers))
	h.pci = clampPCI(h.pci + (100.0-h.pci)*frac)
	if h.closed && h.pci > ClosureEpsilon {
		h.closed = false
	}
	return true
}

// SetBudget sets the annual funding rate and recomputes the macro insolvency year
// by running the real forecast engine (worst-first, current cohorts, this rate).
func (g *Game) SetBudget(rate float64) {
	// Ignore non-finite rates: a NaN/Inf rate would poison the treasury on the
	// next Tick (treasury += budgetRate*dt). There is no debt model in the game,
	// so a negative rate is clamped to 0 rather than bleeding the treasury below
	// zero over ticks.
	if !finite(rate) {
		return
	}
	if rate < 0 {
		rate = 0
	}
	g.budgetRate = rate
	g.recomputeInsolvency()
}

// recomputeInsolvency runs forecast.Simulate (worst-first) with the configured
// cohorts at the current budgetRate and caches InsolvencyYear's verdict.
func (g *Game) recomputeInsolvency() {
	g.insolvencyYear, g.insolvencyOK = insolvencyFromForecast(
		g.cohorts, g.initialPCI, g.growthRate, g.tiers, g.cycleYears, g.horizon, g.budgetRate)
}

// ProjectInsolvency runs the same worst-first macro forecast as
// (*Game).recomputeInsolvency but straight from a Config, without building a
// board. It backs the pregame budget preview: given the cohorts, cost tiers,
// horizon, and a candidate annual budget, it returns the projected insolvency
// year (year, true) or (0, false) when the network stays solvent through the
// horizon. cfg.Hexes is ignored, so no validation/New is required.
func ProjectInsolvency(cfg Config) (year int, ok bool) {
	return insolvencyFromForecast(
		cfg.Cohorts, cfg.InitialPCI, cfg.GrowthRate, cfg.CostTiers,
		forecast.ResolveCycleYears(cfg.TreatmentCycleYears), cfg.HorizonYears, cfg.StartingBudget)
}

// insolvencyFromForecast is the shared forecast core behind recomputeInsolvency
// (live) and ProjectInsolvency (pregame preview): it builds the cohorts, runs
// forecast.Simulate worst-first at the given annual budget over the horizon, and
// returns InsolvencyYear's verdict. An empty cohort set is treated as solvent
// (0, false) — there is nothing to forecast.
func insolvencyFromForecast(cohortCfgs []CohortConfig, initialPCI, growthRate float64, tiers []forecast.CostTier, cycleYears, horizon, budget float64) (int, bool) {
	if len(cohortCfgs) == 0 {
		return 0, false
	}
	cohorts := make([]forecast.Cohort, len(cohortCfgs))
	for i, c := range cohortCfgs {
		cohorts[i] = forecast.Cohort{
			Classification: c.Classification,
			Area:           c.Area,
			DecayRate:      c.DecayRate,
			InitialPCI:     initialPCI,
		}
	}
	params := forecast.NewParams(growthRate, tiers, cycleYears)
	scenario := forecast.Scenario{
		Name:         "game",
		AnnualBudget: budget,
		Strategy:     forecast.StrategyWorstFirst,
	}
	result := forecast.Simulate(scenario, cohorts, int(horizon), params)
	return forecast.InsolvencyYear(result, cycleYears)
}

// recomputeStatus transitions status. won/lost are terminal. Lose if the network
// PCI falls below the floor, too many hexes have closed, or backlog exceeds the
// reconstruct-the-network ceiling. Win if the horizon is reached still solvent.
func (g *Game) recomputeStatus() {
	if g.status != "running" {
		return
	}
	closed := 0
	for i := range g.hexes {
		if g.hexes[i].closed {
			closed++
		}
	}
	closedFrac := float64(closed) / float64(len(g.hexes))

	if g.networkPCI() < LosePCIFloor ||
		closedFrac > LoseClosedFraction ||
		g.backlog > g.loseBacklogCeiling {
		g.status = "lost"
		return
	}
	// Endless (sandbox) never wins by reaching the horizon — play continues until
	// a loss condition above fires.
	if !g.endless && g.simYears >= g.horizon {
		g.status = "won"
	}
}

// networkPCI is the area-weighted PCI over OPEN hexes (closed hexes contribute
// nothing). Returns 0 when no open road area remains.
func (g *Game) networkPCI() float64 {
	var num, den float64
	for i := range g.hexes {
		h := &g.hexes[i]
		if h.closed {
			continue
		}
		num += h.pci * h.roadArea
		den += h.roadArea
	}
	if den <= 0 {
		return 0
	}
	return num / den
}

// --- StateJSON + delta ---

type changedHex struct {
	ID     string  `json:"id"`
	Band   int     `json:"band"`
	PCI    float64 `json:"pci"`
	Closed bool    `json:"closed"`
}

type stateOut struct {
	Year           float64 `json:"year"`
	Treasury       float64 `json:"treasury"`
	BudgetRate     float64 `json:"budget_rate"`
	NetworkPCI     float64 `json:"network_pci"`
	Backlog        float64 `json:"backlog"`
	InsolvencyYear *int    `json:"insolvency_year"`
	Status         string  `json:"status"`
	// OutOfFunds is true when the treasury can't afford even the cheapest possible
	// treatment on the board (cheapest tier x smallest open hex). The front-end
	// surfaces this as the live "out of funds" HUD warning.
	OutOfFunds bool `json:"out_of_funds"`
	// Run totals for the end-of-game summary (cumulative, not deltas).
	Spent       float64      `json:"spent"`
	Treatments  int          `json:"treatments"`
	ClosedCount int          `json:"closed_count"`
	Changed     []changedHex `json:"changed"`
}

// StateJSON marshals the HUD fields plus a delta: only hexes whose quantized
// color band or closed state changed since the last StateJSON call. The first
// call returns every hex (initial paint). insolvency_year is null when the
// network stays solvent through the horizon.
func (g *Game) StateJSON() ([]byte, error) {
	out := stateOut{
		Year:       g.simYears,
		Treasury:   g.treasury,
		BudgetRate: g.budgetRate,
		NetworkPCI: g.networkPCI(),
		Backlog:    g.backlog,
		Status:     g.status,
		Spent:      g.spent,
		Treatments: g.treatments,
		Changed:    []changedHex{},
	}
	if g.insolvencyOK {
		y := g.insolvencyYear
		out.InsolvencyYear = &y
	}

	minOpenArea := math.Inf(1)
	for i := range g.hexes {
		h := &g.hexes[i]
		if h.closed {
			out.ClosedCount++
		} else if h.roadArea < minOpenArea {
			minOpenArea = h.roadArea
		}
		band := BandForPCI(h.pci)
		if band == h.paintedBand && h.closed == h.paintedClosed {
			continue
		}
		out.Changed = append(out.Changed, changedHex{
			ID:     h.id,
			Band:   band,
			PCI:    h.pci,
			Closed: h.closed,
		})
		h.paintedBand = band
		h.paintedClosed = h.closed
	}

	// Out of funds: at least one open hex remains but the treasury can't cover the
	// cheapest treatment possible (cheapest tier x smallest open hex). A lower
	// bound on cost, so a true verdict means no treatment is affordable anywhere.
	if !math.IsInf(minOpenArea, 1) {
		out.OutOfFunds = g.treasury < g.minTierCost*minOpenArea
	}

	return json.Marshal(out)
}

// BandForPCI quantizes a PCI value (0..100) into one of BandCount equal-width
// bands: band = floor(pci / (100/BandCount)), clamped to [0, BandCount-1].
// Band 0 is the worst condition, BandCount-1 the best. The front-end maps these
// to colors, so both sides must use this exact rule.
func BandForPCI(pci float64) int {
	if pci <= 0 {
		return 0
	}
	band := int(pci / (100.0 / float64(BandCount)))
	if band >= BandCount {
		band = BandCount - 1
	}
	return band
}

// --- helpers ---

func (g *Game) hex(id string) *hexState {
	for i := range g.hexes {
		if g.hexes[i].id == id {
			return &g.hexes[i]
		}
	}
	return nil
}

// tierIndex returns the index of the tier with the given label, or -1.
func (g *Game) tierIndex(label string) int {
	for i := range g.tiers {
		if g.tiers[i].Label == label {
			return i
		}
	}
	return -1
}

// resolveTier maps a treatment request to a tier index. The sentinel "auto"
// picks the tier whose [MinPCI, MaxPCI) band contains pci (the rule that lets
// the brush apply the right treatment per hex); any other value is a literal
// tier label. Returns -1 if nothing matches.
func (g *Game) resolveTier(tier string, pci float64) int {
	if tier == "auto" {
		return g.autoTierIndex(pci)
	}
	return g.tierIndex(tier)
}

// autoTierIndex returns the tier whose [MinPCI, MaxPCI) contains pci, or -1 if
// the tiers don't cover it. MaxPCI is exclusive with a 101 sentinel on the top
// tier so PCI == 100 still matches (see forecast.CostTier).
func (g *Game) autoTierIndex(pci float64) int {
	for i := range g.tiers {
		if pci >= g.tiers[i].MinPCI && pci < g.tiers[i].MaxPCI {
			return i
		}
	}
	return -1
}

// tierCostRank returns the ascending cost rank of tier i (0 = cheapest). Ties on
// CostPerSqM break by original index, so every tier gets a unique rank 0..n-1 —
// this is tier i's position in a stable sort by (cost, index). Without the
// tiebreak, duplicate costs collapse to the same rank and the same PCI lift, and
// the priciest tier might never reach rank n-1 (frac==1.0, restore to 100). When
// several tiers tie for the max cost, only the highest-index one restores to 100.
func (g *Game) tierCostRank(i int) int {
	rank := 0
	for j := range g.tiers {
		if g.tiers[j].CostPerSqM < g.tiers[i].CostPerSqM ||
			(g.tiers[j].CostPerSqM == g.tiers[i].CostPerSqM && j < i) {
			rank++
		}
	}
	return rank
}

// jitterOffset derives a deterministic offset in [-jitter, +jitter] from the
// hex id via FNV-32a, so the starting board varies but is reproducible.
func jitterOffset(id string, jitter float64) float64 {
	if jitter == 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	// map hash to [-1, 1]
	frac := float64(h.Sum32()%10001)/10000.0*2.0 - 1.0
	return frac * jitter
}

func clampPCI(pci float64) float64 {
	if pci < 0 {
		return 0
	}
	if pci > 100 {
		return 100
	}
	return pci
}

// finite reports whether f is a real number (not NaN or ±Inf). Used to reject
// non-finite values at the JS->WASM trust boundary before they can poison sim
// state (simYears, treasury, per-hex PCI) irrecoverably.
func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }
