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
	Area           float64 `json:"area"`
	DecayRate      float64 `json:"decay_rate"`
	TotalSpend     float64 `json:"total_spend"`
	TotalDeficit   float64 `json:"total_deficit"`
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
		rate := DecayRateForClass(in.Classification)
		if overrideDecayRate > 0 && IsRoadClass(in.Classification) {
			rate = ScaleRoadDecay(rate, overrideDecayRate)
		}
		cohorts[i] = Cohort{
			Classification: in.Classification,
			Area:           in.Area,
			DecayRate:      rate,
			InitialPCI:     initialPCI,
		}
	}
	return cohorts
}
