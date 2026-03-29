package forecast

// Cohort represents a group of road segments sharing the same classification.
type Cohort struct {
	Classification string  `json:"classification"`
	AreaSqM        float64 `json:"area_sqm"`
	DecayRate      float64 `json:"decay_rate"`
	InitialPCI     float64 `json:"initial_pci"`
}

// CohortSummary holds end-state results for a single cohort after simulation.
type CohortSummary struct {
	Classification string  `json:"classification"`
	EndPCI         float64 `json:"end_pci"`
	AreaSqM        float64 `json:"area_sqm"`
	DecayRate      float64 `json:"decay_rate"`
	TotalSpend     float64 `json:"total_spend"`
	TotalDeficit   float64 `json:"total_deficit"`
}

// CohortInput is a minimal struct for building cohorts without importing db.
type CohortInput struct {
	Classification string
	AreaSqM        float64
}

// BuildCohorts converts CohortInputs into Cohorts with appropriate decay rates.
// If overrideDecayRate > 0, all cohorts use that rate.
// Returns nil if inputs is empty (caller should create a single-cohort fallback).
func BuildCohorts(inputs []CohortInput, initialPCI, overrideDecayRate float64) []Cohort {
	if len(inputs) == 0 {
		return nil
	}

	cohorts := make([]Cohort, len(inputs))
	for i, in := range inputs {
		rate := DecayRateForClass(in.Classification)
		if overrideDecayRate > 0 {
			rate = overrideDecayRate
		}
		cohorts[i] = Cohort{
			Classification: in.Classification,
			AreaSqM:        in.AreaSqM,
			DecayRate:      rate,
			InitialPCI:     initialPCI,
		}
	}
	return cohorts
}
