package forecast

// Params holds the forecasting components, constructed from config values.
// Shared by the forecast command and export to avoid duplicate construction (DRY).
type Params struct {
	Cost   *TieredCostProjector
	Growth *LinearGrowthEstimator
	// CycleYears is the pavement treatment cycle N: each simulated year ~1/N of
	// the network is scheduled for treatment (see Simulate). 0 means "use
	// DefaultTreatmentCycleYears" — resolved in the forecast core via
	// ResolveCycleYears, NOT in config, so every construction path (export, CLI,
	// WASM bridge, tests) gets a safe non-zero divisor.
	CycleYears float64
}

// NewParams builds forecasting parameters from config values.
func NewParams(growthRate float64, costTiers []CostTier, cycleYears float64) *Params {
	p := &Params{
		Cost:       &TieredCostProjector{},
		Growth:     &LinearGrowthEstimator{AnnualGrowthRate: growthRate},
		CycleYears: cycleYears,
	}
	if len(costTiers) > 0 {
		p.Cost.Tiers = costTiers
	}
	return p
}

// SidewalkCostRatio is the sidewalk-to-road cost ratio implied by the default
// tiers: DefaultSidewalkCostTiers is exactly 0.6× DefaultCostTiers in every
// band (3/5, 30/50, 90/150). See sidewalkCostTiers.
const SidewalkCostRatio = 0.6

// sidewalkCostTiers resolves the cost tiers to use for sidewalks. With no
// custom config tiers it returns the discounted DefaultSidewalkCostTiers. When
// a config supplies custom cost_tiers, those describe a *road* schedule (there
// is no separate sidewalk schedule in config), so sidewalk tiers are derived
// by scaling the custom road tiers by SidewalkCostRatio. Previously the custom
// road tiers were applied to sidewalks at full price, mis-pricing sidewalks at
// road cost (LA/Boston sidewalks ran ~1.7× too high — sidewalk/road eff ≈ 0.98
// instead of ≈ 0.6).
func sidewalkCostTiers(costTiers []CostTier) []CostTier {
	if len(costTiers) == 0 {
		return DefaultSidewalkCostTiers
	}
	scaled := make([]CostTier, len(costTiers))
	for i, t := range costTiers {
		scaled[i] = t
		scaled[i].CostPerSqM = t.CostPerSqM * SidewalkCostRatio
	}
	return scaled
}

// NewParamsForResource builds forecasting parameters with resource-specific defaults.
// Sidewalks get lower cost tiers. Other resource types fall back to NewParams.
func NewParamsForResource(resourceType string, growthRate float64, costTiers []CostTier, cycleYears float64) *Params {
	if resourceType == "sidewalks" {
		return &Params{
			Cost:       &TieredCostProjector{Tiers: sidewalkCostTiers(costTiers)},
			Growth:     &LinearGrowthEstimator{AnnualGrowthRate: growthRate},
			CycleYears: cycleYears,
		}
	}
	return NewParams(growthRate, costTiers, cycleYears)
}
