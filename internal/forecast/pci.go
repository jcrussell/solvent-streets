package forecast

import (
	"maps"
	"math"
	"strings"
)

const ClassResidential = "residential"

// defaultDecayRate is the decay rate of pavement with no class of its own: the
// fallback for an unknown class, and the rate of the aggregate "roads" type.
// Units: per year.
const defaultDecayRate = 0.0245

// highwayDecayRates holds the decay rate of each highway class that is its own
// cohort. Its keys are the classes NormalizeClass keeps.
// Higher k = faster decay. Units: per year.
// The ordering and spacing come from FHWA-RD-01-156 "Long-Term Pavement
// Performance" data: higher-class roads (motorway, trunk) decay slower due to
// thicker pavement sections and more rigorous design standards. The level is
// 0.7x the LTPP-derived table (motorway 0.015 ... service 0.045), fitted with
// the strategy efficiencies to the budgeted MTC 2019-2024 backtest
// (TestBacktest_BudgetedFiveYears, solvent-streets-ern4.29).
var highwayDecayRates = map[string]float64{
	"motorway":       0.0105,
	"trunk":          0.014,
	"primary":        0.0175,
	"secondary":      0.021,
	"tertiary":       0.0245,
	ClassResidential: 0.028,
	"service":        0.0315,
}

// RoadDecayRates holds the decay rates of the road types: every highway class
// plus "roads", the aggregate resource type, which decays at the default rate.
var RoadDecayRates = func() map[string]float64 {
	m := maps.Clone(highwayDecayRates)
	m["roads"] = defaultDecayRate
	return m
}()

// DefaultDecayRates is the full lookup table (roads + non-road + fallback).
var DefaultDecayRates = func() map[string]float64 {
	m := maps.Clone(RoadDecayRates)
	m["sidewalks"] = 0.0175 // 0.7x like the road table; key must match resource.TypeSidewalks ("sidewalks")
	m["default"] = defaultDecayRate
	return m
}()

// ResolveDecayRate applies the default decay rate when k is unset (<= 0). It
// is the one fallback for a decay rate that was not configured: the forecast
// core, the cohort builders and the exported seed and scenarios all resolve
// through it, so they simulate and report the same rate.
func ResolveDecayRate(k float64) float64 {
	if k <= 0 {
		return defaultDecayRate
	}
	return k
}

// ExponentialPCIForecaster models PCI decay as PCI(t) = PCI_0 * exp(-k * t).
type ExponentialPCIForecaster struct {
	DecayRate float64 // k value, resolved through ResolveDecayRate
}

func (f *ExponentialPCIForecaster) Forecast(currentPCI float64, years int) []float64 {
	k := ResolveDecayRate(f.DecayRate)
	// Clamp negative years to 0 (house style: clamp-in-place, not error-return);
	// make([]float64, years) panics on a negative length.
	if years < 0 {
		years = 0
	}
	result := make([]float64, years)
	for i := range years {
		t := float64(i + 1)
		// NOTE: this file feeds the forecast WASM binary. math.Exp/Log/Lgamma
		// can differ by ~1 ULP between the WASM and native builds, so any
		// cross-build comparison of these outputs must use a tolerance, not
		// exact equality.
		pci := currentPCI * math.Exp(-k*t)
		if pci < 0 {
			pci = 0
		}
		result[i] = pci
	}
	return result
}

// decayOneStep returns the PCI after a single year of decay. It is the scalar,
// allocation-free equivalent of Forecast(currentPCI, 1)[0] — Simulate's inner
// loop calls it once per cohort per year, so avoiding the throwaway 1-element
// slice matters. The k-resolution and clamp mirror Forecast exactly so the two
// paths are numerically identical.
func (f *ExponentialPCIForecaster) decayOneStep(currentPCI float64) float64 {
	k := ResolveDecayRate(f.DecayRate)
	pci := currentPCI * math.Exp(-k)
	if pci < 0 {
		pci = 0
	}
	return pci
}

// parentClass strips one trailing _link, so a link ramp ("motorway_link")
// reads as the class it connects ("motorway").
func parentClass(highway string) string {
	return strings.TrimSuffix(highway, "_link")
}

// NormalizeClass maps a highway tag value to the cohort class it is filed
// under: a link ramp's parent class (parentClass), kept when it is a highway
// class with its own decay rate (highwayDecayRates), and residential for
// everything else.
func NormalizeClass(highway string) string {
	class := parentClass(highway)
	if _, ok := highwayDecayRates[class]; ok {
		return class
	}
	return ClassResidential
}

// DecayRateForClass returns the decay rate for a classification, a link ramp
// taking its parent class's rate (parentClass). Unlike NormalizeClass, unknown
// values are NOT collapsed to residential — they get the default rate, so
// non-road classes (e.g. "sidewalks") keep their own entry.
func DecayRateForClass(highway string) float64 {
	if k, ok := DefaultDecayRates[parentClass(highway)]; ok {
		return k
	}
	return defaultDecayRate
}
