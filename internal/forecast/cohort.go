package forecast

// Cohort represents a group of road segments sharing the same classification.
type Cohort struct {
	Classification string  `json:"classification"`
	Area           float64 `json:"area"`
	DecayRate      float64 `json:"decay_rate"`
	InitialPCI     float64 `json:"initial_pci"`
}

// CohortSummary holds end-state results for a single cohort after simulation.
type CohortSummary struct {
	Classification string  `json:"classification"`
	EndPCI         float64 `json:"end_pci"`
	// Area is the cohort's final-year (grown) area, matching EndPCI's terminal
	// semantics — not the year-0 input area, and not the multi-year area basis
	// TotalSpend accumulated against. Falls back to the year-0 area when the
	// simulation ran zero years. See Simulate in scenario.go.
	Area         float64 `json:"area"`
	DecayRate    float64 `json:"decay_rate"`
	TotalSpend   float64 `json:"total_spend"`
	TotalDeficit float64 `json:"total_deficit"`
}

// CohortInput is a minimal struct for building cohorts without importing db.
type CohortInput struct {
	Classification string
	Area           float64
}

// IsRoadClass returns true if the classification is a road type (not parking or sidewalks).
func IsRoadClass(classification string) bool {
	_, ok := RoadDecayRates[classification]
	return ok
}

// roadDecayAnchor is the aggregate-road decay rate a config decay_rate override
// is scaled around. Sourced from the table (RoadDecayRates["roads"]) so it can't
// drift from the per-class defaults. Because an override applied to this anchor
// yields the override itself, decay_rate reads as "the rate for a typical road".
var roadDecayAnchor = RoadDecayRates["roads"]

// ScaleRoadDecay returns a road class's decay rate adjusted for a config
// decay_rate override. The override scales every road class proportionally
// around roadDecayAnchor, so the per-class ordering (motorway slowest, service
// fastest) is preserved while the whole curve shifts to the climate-tuned
// level — rather than flattening every class to a single rate. Non-road
// classes never reach here (callers gate on IsRoadClass).
func ScaleRoadDecay(classRate, overrideDecayRate float64) float64 {
	return classRate * (overrideDecayRate / roadDecayAnchor)
}

// ClassDecayRate is the decay rate a cohort of classification uses under a
// config decay_rate override (0 = none): road classes scale their default by
// the override (ScaleRoadDecay); parking and sidewalks keep their own rates.
// It is the one rule every path that builds cohorts follows, so forecast.json,
// the dashboard's seeds and the game's hexes agree (solvent-streets-89n4).
func ClassDecayRate(classification string, overrideDecayRate float64) float64 {
	rate := DecayRateForClass(classification)
	if overrideDecayRate > 0 && IsRoadClass(classification) {
		rate = ScaleRoadDecay(rate, overrideDecayRate)
	}
	return rate
}

// BuildCohorts converts CohortInputs into Cohorts with appropriate decay rates.
// If overrideDecayRate > 0, road cohorts scale their class-specific default by
// the override (see ScaleRoadDecay); non-road cohorts (parking, sidewalks)
// retain their class-specific defaults since they have different deterioration
// characteristics.
// Returns nil if inputs is empty (caller should create a single-cohort fallback).
func BuildCohorts(inputs []CohortInput, initialPCI, overrideDecayRate float64) []Cohort {
	if len(inputs) == 0 {
		return nil
	}

	cohorts := make([]Cohort, len(inputs))
	for i, in := range inputs {
		cohorts[i] = Cohort{
			Classification: in.Classification,
			Area:           in.Area,
			DecayRate:      ClassDecayRate(in.Classification, overrideDecayRate),
			InitialPCI:     initialPCI,
		}
	}
	return cohorts
}

// RawCohort is a cohort before the condition spread: a class, its area and its
// already-resolved decay rate. It is what a seed or a single synthetic cohort
// carries; SpreadCohorts turns it into the cohorts Simulate runs on.
type RawCohort struct {
	Classification string
	Area           float64
	DecayRate      float64
}

// SpreadCohorts is the one way to build the cohorts a forecast simulates. Every
// cohort starts at the single mean initialPCI, a decay rate <= 0 resolves to the
// default rate (so a CohortSummary reports the rate actually simulated), and the
// mean is spread into a condition distribution exactly once
// (applyConditionSpread, which is not idempotent). Pair the simulated result
// with CollapseBands for display.
//
// Export, the WASM bridge, the game, validate and the CLI all build cohorts
// through here or CohortsFromInputs, so they cannot drift on the spread or the
// decay fallback (solvent-streets-i55x).
func SpreadCohorts(raw []RawCohort, initialPCI float64) []Cohort {
	if len(raw) == 0 {
		return nil
	}
	cohorts := make([]Cohort, len(raw))
	for i, r := range raw {
		cohorts[i] = Cohort{
			Classification: r.Classification,
			Area:           r.Area,
			DecayRate:      ResolveDecayRate(r.DecayRate),
			InitialPCI:     initialPCI,
		}
	}
	return applyConditionSpread(cohorts)
}

// CohortsFromInputs builds the simulated cohorts for per-class stats: each class
// decays at ClassDecayRate under the config override, then SpreadCohorts applies
// the mean PCI and the condition spread. Returns nil for empty inputs (the caller
// decides on a fallback).
func CohortsFromInputs(inputs []CohortInput, initialPCI, overrideDecayRate float64) []Cohort {
	if len(inputs) == 0 {
		return nil
	}
	raw := make([]RawCohort, len(inputs))
	for i, in := range inputs {
		raw[i] = RawCohort{
			Classification: in.Classification,
			Area:           in.Area,
			DecayRate:      ClassDecayRate(in.Classification, overrideDecayRate),
		}
	}
	return SpreadCohorts(raw, initialPCI)
}

// CollapseBands collapses the per-band FinalCohorts that SpreadCohorts produces
// back to one row per classification, for display. Headline figures already sum
// across the bands and are unaffected.
func CollapseBands(r ScenarioResult) ScenarioResult {
	r.FinalCohorts = AggregateCohortSummariesByClass(r.FinalCohorts)
	return r
}

// CollapseAllBands applies CollapseBands to every result in place and returns
// the slice.
func CollapseAllBands(rs []ScenarioResult) []ScenarioResult {
	for i := range rs {
		rs[i] = CollapseBands(rs[i])
	}
	return rs
}
