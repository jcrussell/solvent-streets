package forecast

import (
	"maps"
	"math"
	"strings"
)

const ClassResidential = "residential"

// RoadDecayRates holds per-class decay rates for road types.
// Higher k = faster decay. Units: per year.
// Derived from FHWA-RD-01-156 "Long-Term Pavement Performance" data:
// higher-class roads (motorway, trunk) decay slower due to thicker pavement
// sections and more rigorous design standards.
var RoadDecayRates = map[string]float64{
	"motorway":    0.015,
	"trunk":       0.020,
	"primary":     0.025,
	"secondary":   0.030,
	"tertiary":    0.035,
	"residential": 0.040,
	"service":     0.045,
	"roads":       0.035, // aggregate resource type, uses default road rate
}

// DefaultDecayRates is the full lookup table (roads + non-road + fallback).
var DefaultDecayRates = func() map[string]float64 {
	m := make(map[string]float64, len(RoadDecayRates)+2)
	maps.Copy(m, RoadDecayRates)
	m["sidewalk"] = 0.025
	m["default"] = 0.035
	return m
}()

type StubPCIForecaster struct{}

var (
	_ PCIForecaster = (*StubPCIForecaster)(nil)
	_ PCIForecaster = (*ExponentialPCIForecaster)(nil)
)

func (s *StubPCIForecaster) Forecast(currentPCI float64, years int) []float64 {
	result := make([]float64, years)
	for i := range result {
		result[i] = currentPCI
	}
	return result
}

// ExponentialPCIForecaster models PCI decay as PCI(t) = PCI_0 * exp(-k * t).
type ExponentialPCIForecaster struct {
	DecayRate float64 // k value; if 0, uses DefaultDecayRates["default"]
}

func (f *ExponentialPCIForecaster) Forecast(currentPCI float64, years int) []float64 {
	k := f.DecayRate
	if k <= 0 {
		k = DefaultDecayRates["default"]
	}
	result := make([]float64, years)
	for i := range years {
		t := float64(i + 1)
		pci := currentPCI * math.Exp(-k*t)
		if pci < 0 {
			pci = 0
		}
		result[i] = pci
	}
	return result
}

// NormalizeClass maps a highway tag value to a canonical classification.
// It strips _link suffixes and maps uncommon values to their parent class.
func NormalizeClass(highway string) string {
	// Strip _link suffix (e.g. "motorway_link" → "motorway")
	if before, ok := strings.CutSuffix(highway, "_link"); ok {
		highway = before
	}

	switch highway {
	case "motorway", "trunk", "primary", "secondary", "tertiary", ClassResidential, "service":
		return highway
	case "living_street", "unclassified":
		return ClassResidential
	default:
		return ClassResidential
	}
}

// DecayRateForClass returns the decay rate for a road classification.
func DecayRateForClass(highway string) float64 {
	if k, ok := DefaultDecayRates[highway]; ok {
		return k
	}
	return DefaultDecayRates["default"]
}
