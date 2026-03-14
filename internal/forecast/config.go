package forecast

// Params holds the three forecasting components, constructed from config values.
// Shared by the forecast command and export to avoid duplicate construction (DRY).
type Params struct {
	PCI    *ExponentialPCIForecaster
	Cost   *TieredCostProjector
	Growth *LinearGrowthEstimator
}

// NewParams builds forecasting parameters from config values.
func NewParams(decayRate, growthRate float64, costTiers []CostTier) *Params {
	p := &Params{
		PCI:    &ExponentialPCIForecaster{DecayRate: decayRate},
		Cost:   &TieredCostProjector{},
		Growth: &LinearGrowthEstimator{AnnualGrowthRate: growthRate},
	}
	if len(costTiers) > 0 {
		p.Cost.Tiers = costTiers
	}
	return p
}

// NewParamsForResource builds forecasting parameters with resource-specific defaults.
// Sidewalks get a slower decay rate and lower cost tiers. Other resource types
// fall back to the standard NewParams behavior.
func NewParamsForResource(resourceType string, decayRate, growthRate float64, costTiers []CostTier) *Params {
	if resourceType == "sidewalks" {
		if decayRate <= 0 {
			decayRate = DefaultDecayRates["sidewalk"]
		}
		p := &Params{
			PCI:    &ExponentialPCIForecaster{DecayRate: decayRate},
			Cost:   &TieredCostProjector{Tiers: DefaultSidewalkCostTiers},
			Growth: &LinearGrowthEstimator{AnnualGrowthRate: growthRate},
		}
		if len(costTiers) > 0 {
			p.Cost.Tiers = costTiers
		}
		return p
	}
	return NewParams(decayRate, growthRate, costTiers)
}
