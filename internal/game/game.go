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
	status     string // "running" | "won" | "lost"

	cost       *forecast.TieredCostProjector
	tiers      []forecast.CostTier
	initialPCI float64

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
		status:     "running",
		cost:       &forecast.TieredCostProjector{Tiers: tiers},
		tiers:      tiers,
		initialPCI: cfg.InitialPCI,
		cohorts:    cfg.Cohorts,
		growthRate: cfg.GrowthRate,
		cycleYears: forecast.ResolveCycleYears(cfg.TreatmentCycleYears),
	}

	maxCost := 0.0
	for _, t := range tiers {
		if t.CostPerSqM > maxCost {
			maxCost = t.CostPerSqM
		}
	}

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
	if dt <= 0 || g.status != "running" {
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
	if g.status != "running" {
		return false
	}
	ti := g.tierIndex(tier)
	if ti < 0 {
		return false
	}
	h := g.hex(hexID)
	if h == nil {
		return false
	}

	cost := g.tiers[ti].CostPerSqM * h.roadArea
	if g.treasury < cost {
		return false
	}
	g.treasury -= cost

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

	g.recomputeStatus()
	return true
}

// SetBudget sets the annual funding rate and recomputes the macro insolvency year
// by running the real forecast engine (worst-first, current cohorts, this rate).
func (g *Game) SetBudget(rate float64) {
	g.budgetRate = rate
	g.recomputeInsolvency()
}

// recomputeInsolvency runs forecast.Simulate (worst-first) with the configured
// cohorts at the current budgetRate and caches InsolvencyYear's verdict.
func (g *Game) recomputeInsolvency() {
	if len(g.cohorts) == 0 {
		g.insolvencyYear, g.insolvencyOK = 0, false
		return
	}
	cohorts := make([]forecast.Cohort, len(g.cohorts))
	for i, c := range g.cohorts {
		cohorts[i] = forecast.Cohort{
			Classification: c.Classification,
			Area:           c.Area,
			DecayRate:      c.DecayRate,
			InitialPCI:     g.initialPCI,
		}
	}
	params := forecast.NewParams(g.growthRate, g.tiers, g.cycleYears)
	scenario := forecast.Scenario{
		Name:         "game",
		AnnualBudget: g.budgetRate,
		Strategy:     forecast.StrategyWorstFirst,
	}
	result := forecast.Simulate(scenario, cohorts, int(g.horizon), params)
	g.insolvencyYear, g.insolvencyOK = forecast.InsolvencyYear(result, g.cycleYears)
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
	if g.simYears >= g.horizon {
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
	Year           float64      `json:"year"`
	Treasury       float64      `json:"treasury"`
	BudgetRate     float64      `json:"budget_rate"`
	NetworkPCI     float64      `json:"network_pci"`
	Backlog        float64      `json:"backlog"`
	InsolvencyYear *int         `json:"insolvency_year"`
	Status         string       `json:"status"`
	Changed        []changedHex `json:"changed"`
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
		Changed:    []changedHex{},
	}
	if g.insolvencyOK {
		y := g.insolvencyYear
		out.InsolvencyYear = &y
	}

	for i := range g.hexes {
		h := &g.hexes[i]
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

// tierCostRank returns the ascending cost rank of tier i (0 = cheapest).
func (g *Game) tierCostRank(i int) int {
	rank := 0
	for j := range g.tiers {
		if g.tiers[j].CostPerSqM < g.tiers[i].CostPerSqM {
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
