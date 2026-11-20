// Package game holds all state and rules for the Solvent Streets real-time
// pavement-management RTS. It is pure Go and build-tag-free so it can be unit
// tested on the host; a thin syscall/js shim (built separately) drives it from
// the browser; the same cmd/wasm/pvmt shim drives internal/forecast/bridge.
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
	"slices"

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

	// LoseClosedFraction: if more than this share of the board's road area has
	// closed, the game is lost.
	LoseClosedFraction = 0.5

	// SliverAreaShare is the share of the median board hex's road area below
	// which a hex is a sliver: the tip of a road clipped by the hex edge, a
	// target the brush rarely means to treat. The out-of-funds test leaves
	// slivers out, or a city whose board holds one would never be out of funds.
	SliverAreaShare = 0.05

	// DefaultPCIJitter is how far a hex's starting PCI may sit above or below
	// the initial PCI when a Config leaves pci_jitter out, so the board opens
	// as a patchwork instead of one flat colour.
	DefaultPCIJitter = 8.0
)

// --- Config: the JSON contract decoded from the browser at gameInit ---

// HexConfig is a single board hex: a stable id, its roads area in m^2, and
// its area-blended decay rate k (per year).
type HexConfig struct {
	ID        string  `json:"id"`
	RoadsArea float64 `json:"roads_area"`
	DecayRate float64 `json:"decay_rate"`
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
	// Omitted (nil) it is DefaultPCIJitter; 0 starts every hex at InitialPCI.
	PCIJitter *float64 `json:"pci_jitter,omitempty"`

	CostTiers []forecast.CostTier `json:"cost_tiers"`

	// CostMultiplier is the loaded-cost multiplier on every tier, as the
	// forecast prices work (forecast_seed.json's cost_multiplier). It scales the
	// treatment charge, the board's need and the macro forecast alike, so the
	// game spends the dollars Financials budgets. 0 means bare (1.0).
	CostMultiplier float64 `json:"cost_multiplier"`

	// AnnualBudget is the annual funding rate ($/sim-year) that refills the
	// treasury and drives the macro insolvency headline: the forecast's
	// annual_budget.
	AnnualBudget float64 `json:"annual_budget"`

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
	// projection, not as a finish line. A huge HorizonYears must NOT be used
	// to fake endless: ProjectInsolvency runs a per-year forecast over it.
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

// Game holds all sim state. Returned as a concrete *Game.
type Game struct {
	hexes []hexState
	// hexByID indexes hexes by id for treatOne's per-brush-hex lookup; a linear
	// scan there is O(hexes) per treated hex per mousemove. Pointers into g.hexes
	// stay valid because the slice is never grown after New.
	hexByID map[string]*hexState

	treasury     float64
	annualBudget float64
	simYears     float64
	horizon      float64
	endless      bool   // when true, never auto-win at the horizon (sandbox)
	status       string // "running" | "won" | "lost"
	// lossReason names the condition that lost the game (one of the
	// LossXxx constants); empty unless status is "lost".
	lossReason string

	// run totals for the end-of-game summary
	spent      float64 // cumulative $ debited by treatments
	treatments int     // count of successful treatments

	cost       *forecast.TieredCostProjector
	tiers      []forecast.CostTier
	tierRanks  []int // ascending cost rank per tier (see tierCostRank); fixed at New
	initialPCI float64

	// totalArea is the board's road area, fixed at New.
	totalArea float64

	// sliverArea is the road area below which a hex is a sliver
	// (SliverAreaShare of the median hex), left out of the out-of-funds test.
	sliverArea float64

	// cycleYears is the treatment cycle, resolved.
	cycleYears float64

	// Insolvency, on the forecast's definition (forecast.InsolvencyThreshold)
	// applied to the player's own network. Each sim-year the hold-steady need of
	// the board (every hex's tiered cost at its current PCI, over the treatment
	// cycle) accrues in yearNeed and the player's treatments in yearSpent; at
	// the year boundary the shortfall joins backlog, which only ever grows. The
	// game is lost when backlog reaches insolvencyAt, one cycle of the board's
	// year-1 need — the threshold the pregame projection reports against.
	backlog      float64 // cumulative unmet need (forecast DeferredBacklog)
	yearNeed     float64
	yearSpent    float64
	yearsDone    int // completed sim-years already settled into backlog
	insolvencyAt float64
}

// inputNames is what the game's JSON calls the inputs forecast.Inputs
// validates, where the key differs from the forecast package's name.
var inputNames = map[string]string{
	forecast.FieldYears:  "horizon_years",
	forecast.FieldBudget: "annual_budget",
}

// forecastInputs is the macro forecast's view of cfg at the given annual
// budget: what New validates and what ProjectInsolvency simulates.
func (cfg Config) forecastInputs(budget float64) forecast.Inputs {
	raw := make([]forecast.RawCohort, len(cfg.Cohorts))
	for i, c := range cfg.Cohorts {
		raw[i] = forecast.RawCohort{
			Classification: c.Classification,
			Area:           c.Area,
			DecayRate:      c.DecayRate,
		}
	}
	return forecast.Inputs{
		InitialPCI:     cfg.InitialPCI,
		GrowthRate:     cfg.GrowthRate,
		Years:          cfg.HorizonYears,
		Budget:         budget,
		CycleYears:     cfg.TreatmentCycleYears,
		CostTiers:      cfg.CostTiers,
		CostMultiplier: cfg.CostMultiplier,
		Cohorts:        raw,
	}
}

// loadedTiers returns a copy of tiers with every cost scaled by multiplier (a
// value <= 0 is bare, as forecast.TieredCostProjector reads it). The game
// charges treatments, bounds affordability and prices the board's need off
// these, so all three carry the multiplier the macro forecast applies.
func loadedTiers(tiers []forecast.CostTier, multiplier float64) []forecast.CostTier {
	if multiplier <= 0 {
		multiplier = 1
	}
	out := make([]forecast.CostTier, len(tiers))
	for i, t := range tiers {
		t.CostPerSqM *= multiplier
		out[i] = t
	}
	return out
}

// pciJitter is cfg's starting-PCI spread, DefaultPCIJitter when unset.
func (cfg Config) pciJitter() float64 {
	if cfg.PCIJitter == nil {
		return DefaultPCIJitter
	}
	return *cfg.PCIJitter
}

// noDebt is the game's funding rule: there is no debt model, so a negative
// annual rate is no funding at all rather than a drain on the treasury.
func noDebt(rate float64) float64 {
	if rate < 0 {
		return 0
	}
	return rate
}

// validateConfig rejects the untrusted JS->WASM Config before New trusts it.
// The forecast inputs are held to forecast.Inputs.Validate, the bounds a
// pvmt.toml is loaded under, so the seed an export wrote always starts a game.
// The rest are the game's own: a board, at least one cost tier (the treatments
// on offer), the jitter, and a finite budget.
//
// Each float check is written so NaN and ±Inf fail it: a bare `<= 0` / `< 0`
// lets NaN through (all comparisons against NaN are false), and a non-finite
// value poisons treasury, per-hex PCI, and the forecast core irrecoverably.
func validateConfig(cfg Config) error {
	if len(cfg.Hexes) == 0 {
		return errors.New("game: config has no hexes")
	}
	if len(cfg.CostTiers) == 0 {
		return errors.New("game: cost_tiers must be non-empty")
	}
	if j := cfg.pciJitter(); !finite(j) || j < 0 {
		return fmt.Errorf("game: pci_jitter must be finite and >= 0, got %g", j)
	}
	if !finite(cfg.AnnualBudget) {
		return fmt.Errorf("game: annual_budget must be finite, got %g", cfg.AnnualBudget)
	}
	if err := cfg.forecastInputs(noDebt(cfg.AnnualBudget)).Validate(inputNames); err != nil {
		return fmt.Errorf("game: %w", err)
	}
	return validateHexes(cfg.Hexes)
}

// validateHexes checks the board: every hex has an id, a positive decay rate
// and a non-negative road area, and the board holds some road. A hex the map
// rounds to 0 m² may sit on the board, but a board of nothing else has no
// network to keep.
func validateHexes(hexes []HexConfig) error {
	var total float64
	for i, h := range hexes {
		if h.ID == "" {
			return fmt.Errorf("game: hex %d has empty id", i)
		}
		if !finite(h.DecayRate) || h.DecayRate <= 0 {
			return fmt.Errorf("game: hex %q has invalid decay_rate %g (want finite > 0)", h.ID, h.DecayRate)
		}
		if !finite(h.RoadsArea) || h.RoadsArea < 0 {
			return fmt.Errorf("game: hex %q has invalid roads_area %g (want finite >= 0)", h.ID, h.RoadsArea)
		}
		total += h.RoadsArea
	}
	if total <= 0 {
		return errors.New("game: the board's hexes hold no road area")
	}
	return nil
}

// New validates cfg (validateConfig) and builds a Game.
//
// Starting PCI per hex is initial_pci offset by a *deterministic* jitter derived
// from an FNV hash of the hex id (range [-pci_jitter, +pci_jitter], clamped to
// [0,100]). No global math/rand is used, so runs and tests are reproducible.
func New(cfg Config) (*Game, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	tiers := loadedTiers(cfg.CostTiers, cfg.CostMultiplier)

	startBudget := noDebt(cfg.AnnualBudget)

	g := &Game{
		treasury:     startBudget,
		annualBudget: startBudget,
		horizon:      cfg.HorizonYears,
		endless:      cfg.Endless,
		status:       statusRunning,
		cost:         &forecast.TieredCostProjector{Tiers: tiers},
		tiers:        tiers,
		initialPCI:   cfg.InitialPCI,
		cycleYears:   forecast.ResolveCycleYears(cfg.TreatmentCycleYears),
	}

	g.hexes = make([]hexState, len(cfg.Hexes))
	g.hexByID = make(map[string]*hexState, len(cfg.Hexes))
	for i, h := range cfg.Hexes {
		pci := clampPCI(cfg.InitialPCI + jitterOffset(h.ID, cfg.pciJitter()))
		g.hexes[i] = hexState{
			id:          h.ID,
			roadArea:    h.RoadsArea,
			k:           h.DecayRate,
			pci:         pci,
			closed:      pci <= ClosureEpsilon,
			paintedBand: -1, // never painted -> first StateJSON emits all hexes
		}
		// First-wins on duplicate ids: the lookup returns the same hex a linear
		// scan of g.hexes would.
		if _, ok := g.hexByID[h.ID]; !ok {
			g.hexByID[h.ID] = &g.hexes[i]
		}
		g.totalArea += h.RoadsArea
	}
	g.sliverArea = SliverAreaShare * medianRoadArea(cfg.Hexes)
	// One cycle of the board's year-1 need, priced as the forecast prices it:
	// each hex at its PCI after one year's decay.
	var year1Need float64
	for i := range g.hexes {
		h := &g.hexes[i]
		year1Need += g.holdSteadyNeed(h.roadArea, h.pci*math.Exp(-h.k))
	}
	g.insolvencyAt = forecast.InsolvencyThreshold(year1Need, g.cycleYears)

	g.tierRanks = make([]int, len(tiers))
	for i := range tiers {
		g.tierRanks[i] = tierCostRank(tiers, i)
	}
	return g, nil
}

// Tick advances sim time by dt sim-years. Each open hex decays continuously by
// exp(-k*dt); because exp composes (prod over a year of exp(-k*dt) == exp(-k*1)),
// repeated ticks land exactly on the annual ExponentialPCIForecaster step with no
// drift. The treasury refills at annualBudget*dt. The board's hold-steady need
// accrues for the tick, and each completed sim-year settles its unmet need
// (need minus the year's treatment spend, if positive) into the backlog. Hexes
// at or below ClosureEpsilon close.
func (g *Game) Tick(dt float64) {
	// Reject non-finite or non-positive dt: a bare dt<=0 guard lets NaN and +Inf
	// through (NaN<=0 and +Inf<=0 are both false), which would poison simYears,
	// treasury, and every hex's PCI with NaN irrecoverably.
	if !finite(dt) || dt <= 0 || g.status != statusRunning {
		return
	}
	g.simYears += dt
	g.treasury += g.annualBudget * dt

	for i := range g.hexes {
		h := &g.hexes[i]
		if h.closed {
			continue
		}
		h.pci *= math.Exp(-h.k * dt)
		if h.pci <= ClosureEpsilon {
			h.pci = 0
			h.closed = true
		}
	}
	// This tick's share of the year's hold-steady need. Closed hexes still need
	// rebuilding, so every hex counts (a closed hex at PCI 0).
	for i := range g.hexes {
		g.yearNeed += g.holdSteadyNeed(g.hexes[i].roadArea, g.hexes[i].pci) * dt
	}
	for float64(g.yearsDone+1) <= g.simYears {
		g.backlog += math.Max(0, g.yearNeed-g.yearSpent)
		g.yearNeed, g.yearSpent = 0, 0
		g.yearsDone++
	}

	g.recomputeStatus()
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
// does, once). If the treasury cannot afford the tier's cost over the hex's
// road area, it is a no-op. Otherwise it debits the treasury and raises the
// hex's PCI toward 100 by a fraction set by the tier's cost rank: the priciest
// tier (reconstruction) restores to 100, the cheapest (preventive) gives the
// smallest lift, and a treatment that lifts a closed hex above ClosureEpsilon
// reopens it. tier is a cost_tiers label, or the sentinel "auto" to let the
// engine pick the tier whose [MinPCI, MaxPCI) band contains the hex's current
// PCI — so the front-end never has to choose preventive/rehab/reconstruction per
// hex. Returns false (no-op) if the game is over, the tier/hex is unknown, or the
// treasury can't afford it. Accumulates spent/treatments for the summary.
func (g *Game) treatOne(hexID, tier string) bool {
	if g.status != statusRunning {
		return false
	}
	h := g.hexByID[hexID]
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
	g.yearSpent += cost
	g.treatments++

	// Lift toward 100 by (rank+1)/n of the remaining headroom; top tier -> 100.
	frac := float64(g.tierRanks[ti]+1) / float64(len(g.tiers))
	h.pci = clampPCI(h.pci + (100.0-h.pci)*frac)
	if h.closed && h.pci > ClosureEpsilon {
		h.closed = false
	}
	return true
}

// ProjectInsolvency runs the worst-first macro forecast straight from a
// Config, without building a board. It backs the pregame budget preview:
// given the cohorts, cost tiers,
// horizon, and a candidate annual budget, it returns the projected insolvency
// year (year, true) or (0, false) when the network stays solvent through the
// horizon. cfg.Hexes is ignored, so no validation/New is required.
func ProjectInsolvency(cfg Config) (year int, ok bool) {
	return insolvencyFromForecast(cfg.forecastInputs(noDebt(cfg.AnnualBudget)))
}

// insolvencyFromForecast is the forecast core behind ProjectInsolvency: it
// spreads the cohorts, runs forecast.Simulate worst-first at in.Budget over
// the horizon, and returns InsolvencyYear's verdict. An empty cohort set is
// treated as solvent (0, false) — there is nothing to forecast.
//
// It is reached straight from an untrusted Config (the WASM
// gameProjectInsolvency export) without going through New, so the inputs are
// validated here, against the bounds New holds a Config to. Inputs outside them return the
// "no result" signal (0, false) rather than a garbage preview or, for the
// horizon, a crash: forecast.Simulate allocates per year, so a non-positive
// horizon panics and a huge one exhausts the WASM runtime.
func insolvencyFromForecast(in forecast.Inputs) (int, bool) {
	if len(in.Cohorts) == 0 || in.Validate(nil) != nil {
		return 0, false
	}
	// SpreadCohorts spreads the single mean InitialPCI into a condition
	// distribution, through the same constructor as export, the WASM bridge and
	// the CLI. The flat single-average model understates program cost ~32-37%
	// (condition.go), so the game's insolvency headline must not skip it.
	cohorts := forecast.SpreadCohorts(in.Cohorts, in.InitialPCI)
	cycleYears := forecast.ResolveCycleYears(in.CycleYears)
	params := forecast.NewParams(in.GrowthRate, in.CostTiers, cycleYears, in.CostMultiplier)
	scenario := forecast.Scenario{
		Name:         "game",
		AnnualBudget: in.Budget,
		Strategy:     forecast.StrategyWorstFirst,
	}
	result := forecast.Simulate(scenario, cohorts, int(in.Years), params)
	return forecast.InsolvencyYear(result, cycleYears)
}

// The states a game is in, reported as StateJSON's status.
const (
	statusRunning = "running"
	statusWon     = "won"
	statusLost    = "lost"
)

// The reasons a game is lost, reported as StateJSON's loss_reason so the
// end banner can say which condition ended it.
const (
	LossPCIFloor = "pci_floor" // network PCI fell below LosePCIFloor
	LossClosures = "closures"  // more than LoseClosedFraction of road area closed
	LossBacklog  = "backlog"   // deferred backlog reached insolvency
)

// recomputeStatus transitions status. won/lost are terminal. Lose if the network
// PCI falls below the floor, too much road area has closed, or the deferred
// backlog reaches one treatment cycle of year-1 need (insolvent, the same
// definition the pregame projection and the Financials headline use). Win if
// the horizon is reached still solvent.
func (g *Game) recomputeStatus() {
	if g.status != statusRunning {
		return
	}
	switch {
	case g.networkPCI() < LosePCIFloor:
		g.lossReason = LossPCIFloor
	case g.closedShare() > LoseClosedFraction:
		g.lossReason = LossClosures
	case g.insolvencyAt > 0 && g.backlog >= g.insolvencyAt:
		g.lossReason = LossBacklog
	}
	if g.lossReason != "" {
		g.status = statusLost
		return
	}
	// Endless (sandbox) never wins by reaching the horizon — play continues until
	// a loss condition above fires.
	if !g.endless && g.simYears >= g.horizon {
		g.status = statusWon
	}
}

// closedShare is the share of the board's road area that has closed, the
// measure the closures loss and the end banner's gravel figure read. Area, not
// a hex count, so a closed sliver weighs what it holds, as it does in the
// network PCI. New refuses a board with no road area, so the share is
// always defined.
func (g *Game) closedShare() float64 {
	var closedArea float64
	for i := range g.hexes {
		if g.hexes[i].closed {
			closedArea += g.hexes[i].roadArea
		}
	}
	return closedArea / g.totalArea
}

// holdSteadyNeed is one hex's annual hold-steady need at pci: treating its
// scheduled 1/N slice at the tiered cost of its condition, exactly as
// forecast.Simulate prices a cohort's need.
func (g *Game) holdSteadyNeed(area, pci float64) float64 {
	return g.cost.ProjectCost(area, pci) / g.cycleYears
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
	ID     string `json:"id"`
	Band   int    `json:"band"`
	Closed bool   `json:"closed"`
}

type stateOut struct {
	Year         float64 `json:"year"`
	Treasury     float64 `json:"treasury"`
	AnnualBudget float64 `json:"annual_budget"`
	NetworkPCI   float64 `json:"network_pci"`
	Backlog      float64 `json:"deferred_backlog"`
	Status       string  `json:"status"`
	// LossReason is the LossXxx condition that ended a lost game; omitted
	// while the game is running or won.
	LossReason string `json:"loss_reason,omitempty"`
	// OutOfFunds is true when the treasury can't afford the treatment the brush
	// would apply to any hex on the board (see outOfFunds). The front-end
	// surfaces this as the live "out of funds" HUD warning.
	OutOfFunds bool `json:"out_of_funds"`
	// Run totals for the end-of-game summary (cumulative, not deltas).
	Spent      float64 `json:"total_spend"`
	Treatments int     `json:"treatment_count"`
	// ClosedShare is the share of the board's road area that has closed
	// (closedShare), 0..1.
	ClosedShare float64      `json:"closed_share"`
	Changed     []changedHex `json:"changed"`
}

// StateJSON marshals the HUD fields plus a delta: only hexes whose quantized
// color band or closed state changed since the last StateJSON call. The first
// call returns every hex (initial paint).
func (g *Game) StateJSON() ([]byte, error) {
	out := stateOut{
		Year:         g.simYears,
		Treasury:     g.treasury,
		AnnualBudget: g.annualBudget,
		NetworkPCI:   g.networkPCI(),
		Backlog:      g.backlog,
		Status:       g.status,
		LossReason:   g.lossReason,
		Spent:        g.spent,
		Treatments:   g.treatments,
		Changed:      []changedHex{},
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
			Closed: h.closed,
		})
		h.paintedBand = band
		h.paintedClosed = h.closed
	}

	out.OutOfFunds = g.outOfFunds()
	out.ClosedShare = g.closedShare()

	return json.Marshal(out)
}

// outOfFunds reports whether the treasury can't pay for the treatment the
// "auto" brush would apply to any hex that is not a sliver: each hex at the
// tier its condition calls for (a closed hex's is reconstruction), over its
// road area. Slivers are left out because a sliver's treatment costs next to
// nothing, which would hold the verdict false for a city whose board holds
// one; a board of nothing but slivers is judged on them all. A hex with no
// road area costs nothing to treat and is never judged.
func (g *Game) outOfFunds() bool {
	cheapest, cheapestAny := math.Inf(1), math.Inf(1)
	for i := range g.hexes {
		h := &g.hexes[i]
		ti := g.autoTierIndex(h.pci)
		if ti < 0 || h.roadArea <= 0 {
			continue
		}
		cost := g.tiers[ti].CostPerSqM * h.roadArea
		cheapestAny = math.Min(cheapestAny, cost)
		if h.roadArea >= g.sliverArea {
			cheapest = math.Min(cheapest, cost)
		}
	}
	if math.IsInf(cheapest, 1) {
		cheapest = cheapestAny
	}
	return !math.IsInf(cheapest, 1) && g.treasury < cheapest
}

// medianRoadArea is the median road area over the hexes that hold road (0
// for none), so a board where most hexes round to 0 m² still has a sliver
// threshold.
func medianRoadArea(hexes []HexConfig) float64 {
	var areas []float64
	for _, h := range hexes {
		if h.RoadsArea > 0 {
			areas = append(areas, h.RoadsArea)
		}
	}
	if len(areas) == 0 {
		return 0
	}
	slices.Sort(areas)
	mid := len(areas) / 2
	if len(areas)%2 == 1 {
		return areas[mid]
	}
	return (areas[mid-1] + areas[mid]) / 2
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
// tier label. Returns -1 for a label no tier has.
func (g *Game) resolveTier(tier string, pci float64) int {
	if tier == "auto" {
		return g.autoTierIndex(pci)
	}
	return g.tierIndex(tier)
}

// autoTierIndex returns the tier the forecast would treat pci with
// (forecast.TierIndexForPCI): the tier whose [MinPCI, MaxPCI) band contains
// it, or the nearest band where the tiers leave a gap. -1 only with no tiers.
func (g *Game) autoTierIndex(pci float64) int {
	return forecast.TierIndexForPCI(g.tiers, pci)
}

// tierCostRank returns the ascending cost rank of tier i (0 = cheapest). Ties on
// CostPerSqM break by original index, so every tier gets a unique rank 0..n-1 —
// this is tier i's position in a stable sort by (cost, index). Without the
// tiebreak, duplicate costs collapse to the same rank and the same PCI lift, and
// the priciest tier might never reach rank n-1 (frac==1.0, restore to 100). When
// several tiers tie for the max cost, only the highest-index one restores to 100.
// Tiers are immutable after New, so ranks are computed once there (g.tierRanks).
func tierCostRank(tiers []forecast.CostTier, i int) int {
	rank := 0
	for j := range tiers {
		if tiers[j].CostPerSqM < tiers[i].CostPerSqM ||
			(tiers[j].CostPerSqM == tiers[i].CostPerSqM && j < i) {
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
