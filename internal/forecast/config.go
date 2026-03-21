package forecast

// Params holds the forecasting components, constructed from config values.
// Shared by the forecast command and export to avoid duplicate construction (DRY).
type Params struct {
	Cost   *TieredCostProjector
	Growth *LinearGrowthEstimator
}

// NewParams builds forecasting parameters from config values.
func NewParams(growthRate float64, costTiers []CostTier) *Params {
	p := &Params{
		Cost:   &TieredCostProjector{},
		Growth: &LinearGrowthEstimator{AnnualGrowthRate: growthRate},
	}
	if len(costTiers) > 0 {
		p.Cost.Tiers = costTiers
	}
	return p
}

// NewParamsForResource builds forecasting parameters with resource-specific defaults.
// Sidewalks get lower cost tiers. Other resource types fall back to NewParams.
func NewParamsForResource(resourceType string, growthRate float64, costTiers []CostTier) *Params {
	if resourceType == "sidewalks" {
		p := &Params{
			Cost:   &TieredCostProjector{Tiers: DefaultSidewalkCostTiers},
			Growth: &LinearGrowthEstimator{AnnualGrowthRate: growthRate},
		}
		if len(costTiers) > 0 {
			p.Cost.Tiers = costTiers
		}
		return p
	}
	return NewParams(growthRate, costTiers)
}
