package forecast

import (
	"cmp"
	"fmt"
	"math"
	"slices"
)

// The bounds on forecast inputs. Every surface that accepts them checks them
// through Inputs.Validate: the config loader (pvmt.toml), the browser forecast
// bridge and the game. A site's forecast seed is built from a loaded config,
// so one set of bounds means a config that exports cannot produce a seed the
// browser then refuses.
const (
	// MaxPCI is the top of the PCI scale; a starting PCI lies in [0, MaxPCI].
	MaxPCI = 100.0
	// MaxTierPCI bounds a cost tier's max_pci. It is one above MaxPCI because
	// a tier's band is half-open, [min_pci, max_pci): the top tier states 101
	// so that PCI 100 falls inside it.
	MaxTierPCI = 101.0
	// MaxDecayRate bounds the single decay rate k of a typical road. A
	// cohort's own rate has no upper bound, because ScaleRoadDecay scales each
	// road class around that rate and a fast class can land above it.
	MaxDecayRate = 1.0
	// MinGrowthRate and MaxGrowthRate bound the linear annual area growth.
	MinGrowthRate = -0.5
	MaxGrowthRate = 0.5
	// MaxHorizonYears bounds the forecast horizon. Simulate allocates per
	// year, so an unbounded horizon exhausts the WASM runtime's memory.
	MaxHorizonYears = 200
	// MinTreatmentCycleYears and MaxTreatmentCycleYears bound the treatment
	// cycle N. Simulate treats 1/N of the network a year, so a cycle below one
	// year makes more than the whole network eligible and reports insolvency
	// in year 1 at any budget.
	MinTreatmentCycleYears = 1.0
	MaxTreatmentCycleYears = 40.0
	// MaxCostMultiplier bounds the loaded-cost multiplier. Documented load
	// stacks run near 1.5; anything past 5 is a decimal slip or a percentage.
	MaxCostMultiplier = 5.0
)

// The names Inputs.Validate gives each input in an InputError. A caller whose
// own vocabulary differs passes its names to Validate.
const (
	FieldInitialPCI     = "initial_pci"
	FieldDecayRate      = "decay_rate"
	FieldGrowthRate     = "growth_rate"
	FieldHorizonYears   = "horizon_years"
	FieldBudget         = "budget"
	FieldCycleYears     = "treatment_cycle_years"
	FieldCostMultiplier = "cost_multiplier"
	FieldCostTiers      = "cost_tiers"
	FieldCohorts        = "cohorts"
)

// Inputs is everything a forecast run is parameterised by, in the form the
// bounds are stated on.
type Inputs struct {
	// InitialPCI is the mean starting condition, in [0, MaxPCI].
	InitialPCI float64
	// DecayRate is the decay rate of a typical road, in [0, MaxDecayRate];
	// 0 is unset (ResolveDecayRate).
	DecayRate float64
	// GrowthRate lies in [MinGrowthRate, MaxGrowthRate].
	GrowthRate float64
	// Years is the horizon, in [1, MaxHorizonYears]. A float because the game
	// counts fractional years; a caller that reads 0 as "use my default"
	// resolves it before validating.
	Years float64
	// Budget is the annual budget in dollars, finite and >= 0.
	Budget float64
	// CycleYears is the treatment cycle, in [MinTreatmentCycleYears,
	// MaxTreatmentCycleYears]; 0 is unset (ResolveCycleYears).
	CycleYears float64
	// CostMultiplier is the loaded-cost multiplier, in (0, MaxCostMultiplier];
	// 0 is unset.
	CostMultiplier float64
	// CostTiers may be empty (DefaultCostTiers apply).
	CostTiers []CostTier
	// Cohorts may be empty.
	Cohorts []RawCohort
}

// InputError reports the one input Inputs.Validate rejected.
type InputError struct {
	// Field names the input: a Field* constant, or the caller's name for it.
	Field string
	// Index is the element of a collection field (cost tiers, cohorts) that
	// failed, or -1 for a scalar.
	Index int
	// Label is that element's label or classification.
	Label string
	// Problem says what is wrong, value included, without naming the field.
	Problem string
}

func (e *InputError) Error() string {
	switch {
	case e.Index < 0:
		return e.Field + " " + e.Problem
	case e.Label == "":
		return fmt.Sprintf("%s[%d]: %s", e.Field, e.Index, e.Problem)
	default:
		return fmt.Sprintf("%s[%d] (%s): %s", e.Field, e.Index, e.Label, e.Problem)
	}
}

// Validate checks every input against the bounds above and returns an
// *InputError for the first one outside them. names maps a Field* constant to
// what the caller calls that input, so the error speaks the caller's
// vocabulary (a TOML key, a JSON key); a field it omits keeps its Field* name.
//
// Every comparison is written so that NaN fails it: NaN is false against any
// ordered comparison, so a bare `v < lo || v > hi` would admit it.
func (in Inputs) Validate(names map[string]string) error {
	err := in.validate()
	if err == nil {
		return nil
	}
	if n, ok := names[err.Field]; ok {
		err.Field = n
	}
	return err
}

func (in Inputs) validate() *InputError {
	scalars := []struct {
		field   string
		ok      bool
		problem string
	}{
		{FieldInitialPCI, between(in.InitialPCI, 0, MaxPCI),
			fmt.Sprintf("%g out of range (0-%g)", in.InitialPCI, MaxPCI)},
		{FieldDecayRate, between(in.DecayRate, 0, MaxDecayRate),
			fmt.Sprintf("%g out of range (0-%g)", in.DecayRate, MaxDecayRate)},
		{FieldGrowthRate, between(in.GrowthRate, MinGrowthRate, MaxGrowthRate),
			fmt.Sprintf("%g out of range (%g to %g)", in.GrowthRate, MinGrowthRate, MaxGrowthRate)},
		{FieldHorizonYears, between(in.Years, 1, MaxHorizonYears),
			fmt.Sprintf("%g out of range (1-%d)", in.Years, MaxHorizonYears)},
		{FieldBudget, nonNegative(in.Budget),
			fmt.Sprintf("%g must be a non-negative finite number", in.Budget)},
		{FieldCostMultiplier, in.CostMultiplier == 0 || (in.CostMultiplier > 0 && in.CostMultiplier <= MaxCostMultiplier),
			fmt.Sprintf("%g out of range (0 = unset, else 0-%g)", in.CostMultiplier, MaxCostMultiplier)},
		{FieldCycleYears, in.CycleYears == 0 || between(in.CycleYears, MinTreatmentCycleYears, MaxTreatmentCycleYears),
			fmt.Sprintf("%g out of range (%g-%g, or 0 for default)", in.CycleYears, MinTreatmentCycleYears, MaxTreatmentCycleYears)},
	}
	for _, s := range scalars {
		if !s.ok {
			return &InputError{Field: s.field, Index: -1, Problem: s.problem}
		}
	}
	for i, t := range in.CostTiers {
		if problem := tierProblem(t); problem != "" {
			return &InputError{Field: FieldCostTiers, Index: i, Label: t.Label, Problem: problem}
		}
	}
	if err := tierOverlap(in.CostTiers); err != nil {
		return err
	}
	for i, c := range in.Cohorts {
		if problem := cohortProblem(c); problem != "" {
			return &InputError{Field: FieldCohorts, Index: i, Label: c.Classification, Problem: problem}
		}
	}
	return nil
}

// tierOverlap reports the first pair of cost tiers whose PCI bands overlap,
// naming the later band in PCI order. A PCI in two bands has two prices, and
// the dashboard refuses to run its interactive forecast on such a schedule,
// so every forecast surface refuses it here, at config load included.
// Gaps between bands stay legal: TierIndexForPCI
// resolves a gap to the nearest band.
func tierOverlap(tiers []CostTier) *InputError {
	order := make([]int, len(tiers))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(tiers[a].MinPCI, tiers[b].MinPCI) })
	for k := 1; k < len(order); k++ {
		prev, cur := tiers[order[k-1]], tiers[order[k]]
		if cur.MinPCI < prev.MaxPCI {
			return &InputError{Field: FieldCostTiers, Index: order[k], Label: cur.Label, Problem: fmt.Sprintf(
				"pci band [%g, %g) overlaps %q [%g, %g)", cur.MinPCI, cur.MaxPCI, prev.Label, prev.MinPCI, prev.MaxPCI)}
		}
	}
	return nil
}

// tierProblem says what is wrong with one cost tier, or "". Tiers feed
// buildAnchors' midpoint interpolation directly, so a non-positive cost, an
// inverted band or an out-of-range bound produces wrong dollar figures with
// nothing to flag them; in the game a free tier is a treatment that costs
// nothing. Overlaps between tiers are tierOverlap's.
func tierProblem(t CostTier) string {
	switch {
	case !(t.CostPerSqM > 0) || math.IsInf(t.CostPerSqM, 0):
		return fmt.Sprintf("cost_per_sqm %g must be a positive finite number", t.CostPerSqM)
	case !finite(t.MinPCI) || !finite(t.MaxPCI):
		return fmt.Sprintf("pci band [%g, %g) must be finite", t.MinPCI, t.MaxPCI)
	case t.MinPCI < 0 || t.MaxPCI > MaxTierPCI:
		return fmt.Sprintf("pci band [%g, %g) out of range (0-%g)", t.MinPCI, t.MaxPCI, MaxTierPCI)
	case t.MinPCI >= t.MaxPCI:
		return fmt.Sprintf("min_pci %g must be less than max_pci %g", t.MinPCI, t.MaxPCI)
	case t.Label == "":
		return "label must not be empty"
	}
	return ""
}

// cohortProblem says what is wrong with one cohort, or "".
func cohortProblem(c RawCohort) string {
	switch {
	case !nonNegative(c.Area):
		return fmt.Sprintf("area %g must be a non-negative finite number", c.Area)
	case !nonNegative(c.DecayRate):
		return fmt.Sprintf("decay_rate %g must be a non-negative finite number", c.DecayRate)
	}
	return ""
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// between reports lo <= v <= hi; false for NaN.
func between(v, lo, hi float64) bool { return v >= lo && v <= hi }

// nonNegative reports a finite v >= 0.
func nonNegative(v float64) bool { return v >= 0 && !math.IsInf(v, 1) }
