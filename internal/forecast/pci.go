package forecast

import (
	"math"
	"strings"
)

// Default decay rates by road classification (FHWA national averages).
// Higher k = faster decay. Units: per year.
var DefaultDecayRates = map[string]float64{
	"motorway":    0.015,
	"trunk":       0.020,
	"primary":     0.025,
	"secondary":   0.030,
	"tertiary":    0.035,
	"residential": 0.040,
	"service":     0.045,
	"default":     0.035,
	"sidewalk":    0.025,
}

type StubPCIForecaster struct{}

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
	if strings.HasSuffix(highway, "_link") {
		highway = strings.TrimSuffix(highway, "_link")
	}

	switch highway {
	case "motorway", "trunk", "primary", "secondary", "tertiary", "residential", "service":
		return highway
	case "living_street", "unclassified":
		return "residential"
	default:
		return "residential"
	}
}

// DecayRateForClass returns the decay rate for a road classification.
func DecayRateForClass(highway string) float64 {
	if k, ok := DefaultDecayRates[highway]; ok {
		return k
	}
	return DefaultDecayRates["default"]
}
