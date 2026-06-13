package units

import "fmt"

// System represents a unit system for display.
type System int

const (
	Metric   System = iota // sq m, hectares, sq km, $/sq m
	Imperial               // sq ft, acres, sq mi, $/sq ft

	// numSystems is the count of declared System values. Tests use it
	// to pin the exhaustiveness coverage list — see units_test.go.
	numSystems
)

// Canonical lowercase spellings of the two unit systems. Kept as named
// constants so goconst doesn't complain about repeated literals in
// ParseSystem / IsKnown / String, and so the user-facing spelling lives
// in exactly one place.
const (
	metricName   = "metric"
	imperialName = "imperial"
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
	case metricName, "Metric", "METRIC":
		return Metric
	default:
		return Imperial
	}
}

// IsKnown reports whether s names a recognized unit system. The empty string
// is not known (it maps to Imperial by default in ParseSystem, but callers
// that want to distinguish "unset" from "explicitly imperial" need this).
func IsKnown(s string) bool {
	switch s {
	case metricName, "Metric", "METRIC", imperialName, "Imperial", "IMPERIAL":
		return true
	default:
		return false
	}
}

func (s System) String() string {
	if s == Metric {
		return metricName
	}
	return imperialName
}
