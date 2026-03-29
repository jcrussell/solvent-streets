package units

import "fmt"

// System represents a unit system for display.
type System int

const (
	Metric   System = iota // sq m, hectares, sq km, $/sq m
	Imperial               // sq ft, acres, sq mi, $/sq ft
)

// Conversion constants.
const (
	SqFtPerSqM   = 10.763910417
	SqMPerHa     = 10_000.0
	SqMPerSqKm   = 1_000_000.0
	SqFtPerAcre  = 43_560.0
	AcresPerSqMi = 640.0
)

// --- Area conversions (from internal sq m) ---

func SqMToSqFt(sqm float64) float64     { return sqm * SqFtPerSqM }
func SqMToAcres(sqm float64) float64    { return SqMToSqFt(sqm) / SqFtPerAcre }
func SqMToSqMi(sqm float64) float64     { return SqMToAcres(sqm) / AcresPerSqMi }
func SqMToHectares(sqm float64) float64 { return sqm / SqMPerHa }
func SqMToSqKm(sqm float64) float64     { return sqm / SqMPerSqKm }

// --- Cost conversions ---

func CostPerSqMToPerSqFt(cpsm float64) float64 { return cpsm / SqFtPerSqM }
func CostPerSqFtToPerSqM(cpsf float64) float64 { return cpsf * SqFtPerSqM }

// --- Display formatting ---

// FormatArea returns area in the base unit of the given system.
func FormatArea(sqm float64, sys System) string {
	if sys == Imperial {
		return fmt.Sprintf("%.0f sq ft", SqMToSqFt(sqm))
	}
	return fmt.Sprintf("%.0f sq m", sqm)
}

// FormatAreaLarge returns area in a larger unit (acres or hectares).
func FormatAreaLarge(sqm float64, sys System) string {
	if sys == Imperial {
		return fmt.Sprintf("%.1f acres", SqMToAcres(sqm))
	}
	return fmt.Sprintf("%.2f ha", SqMToHectares(sqm))
}

// FormatAreaVeryLarge returns area in the largest unit (sq mi or sq km).
func FormatAreaVeryLarge(sqm float64, sys System) string {
	if sys == Imperial {
		return fmt.Sprintf("%.2f sq mi", SqMToSqMi(sqm))
	}
	return fmt.Sprintf("%.2f sq km", SqMToSqKm(sqm))
}

// FormatCostRate returns a formatted cost-per-area string.
func FormatCostRate(costPerSqM float64, sys System) string {
	if sys == Imperial {
		return fmt.Sprintf("$%.2f/sq ft", CostPerSqMToPerSqFt(costPerSqM))
	}
	return fmt.Sprintf("$%.2f/sq m", costPerSqM)
}

// --- Column header labels ---

func AreaLabel(sys System) string {
	if sys == Imperial {
		return "Area (sq ft)"
	}
	return "Area (sq m)"
}

func AreaLargeLabel(sys System) string {
	if sys == Imperial {
		return "Area (acres)"
	}
	return "Area (ha)"
}

func AreaVeryLargeLabel(sys System) string {
	if sys == Imperial {
		return "Area (sq mi)"
	}
	return "Area (sq km)"
}

func CostRateLabel(sys System) string {
	if sys == Imperial {
		return "$/sq ft"
	}
	return "$/sq m"
}

// AreaValue returns the area numeric value in the base unit for the system.
func AreaValue(sqm float64, sys System) float64 {
	if sys == Imperial {
		return SqMToSqFt(sqm)
	}
	return sqm
}

// AreaLargeValue returns the area numeric value in the large unit for the system.
func AreaLargeValue(sqm float64, sys System) float64 {
	if sys == Imperial {
		return SqMToAcres(sqm)
	}
	return SqMToHectares(sqm)
}

// AreaVeryLargeValue returns the area numeric value in the very large unit for the system.
func AreaVeryLargeValue(sqm float64, sys System) float64 {
	if sys == Imperial {
		return SqMToSqMi(sqm)
	}
	return SqMToSqKm(sqm)
}

// ParseSystem converts a string to a System. Returns Imperial for unrecognized values.
func ParseSystem(s string) System {
	switch s {
	case "metric", "Metric", "METRIC":
		return Metric
	default:
		return Imperial
	}
}

func (s System) String() string {
	if s == Metric {
		return "metric"
	}
	return "imperial"
}
