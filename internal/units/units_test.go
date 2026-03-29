package units

import (
	"math"
	"testing"
)

func approxEqual(a, b, tol float64) bool {
	return math.Abs(a-b) < tol
}

func TestSqMToSqFt(t *testing.T) {
	// 1 sq m ≈ 10.764 sq ft
	got := SqMToSqFt(1)
	if !approxEqual(got, 10.7639, 0.001) {
		t.Errorf("SqMToSqFt(1) = %f, want ~10.7639", got)
	}
}

func TestSqMToAcres(t *testing.T) {
	// 1 acre = 4046.86 sq m
	got := SqMToAcres(4046.86)
	if !approxEqual(got, 1.0, 0.01) {
		t.Errorf("SqMToAcres(4046.86) = %f, want ~1.0", got)
	}
}

func TestSqMToSqMi(t *testing.T) {
	// 1 sq mi ≈ 2,589,988 sq m
	got := SqMToSqMi(2_589_988)
	if !approxEqual(got, 1.0, 0.01) {
		t.Errorf("SqMToSqMi(2589988) = %f, want ~1.0", got)
	}
}

func TestSqMToHectares(t *testing.T) {
	got := SqMToHectares(10_000)
	if got != 1.0 {
		t.Errorf("SqMToHectares(10000) = %f, want 1.0", got)
	}
}

func TestSqMToSqKm(t *testing.T) {
	got := SqMToSqKm(1_000_000)
	if got != 1.0 {
		t.Errorf("SqMToSqKm(1000000) = %f, want 1.0", got)
	}
}

func TestCostConversions(t *testing.T) {
	// Round-trip: $/sq m → $/sq ft → $/sq m
	original := 4.20
	sqft := CostPerSqMToPerSqFt(original)
	back := CostPerSqFtToPerSqM(sqft)
	if !approxEqual(back, original, 0.001) {
		t.Errorf("round-trip cost: got %f, want %f", back, original)
	}
}

func TestFormatArea(t *testing.T) {
	got := FormatArea(100, Imperial)
	if got != "1076 sq ft" {
		t.Errorf("FormatArea(100, Imperial) = %q", got)
	}
	got = FormatArea(100, Metric)
	if got != "100 sq m" {
		t.Errorf("FormatArea(100, Metric) = %q", got)
	}
}

func TestFormatAreaLarge(t *testing.T) {
	got := FormatAreaLarge(10_000, Metric)
	if got != "1.00 ha" {
		t.Errorf("FormatAreaLarge(10000, Metric) = %q", got)
	}
}

func TestParseSystem(t *testing.T) {
	if ParseSystem("metric") != Metric {
		t.Error("ParseSystem(metric) should be Metric")
	}
	if ParseSystem("imperial") != Imperial {
		t.Error("ParseSystem(imperial) should be Imperial")
	}
	if ParseSystem("") != Imperial {
		t.Error("ParseSystem('') should default to Imperial")
	}
}

func TestSystemString(t *testing.T) {
	if Metric.String() != "metric" {
		t.Error("Metric.String() should be 'metric'")
	}
	if Imperial.String() != "imperial" {
		t.Error("Imperial.String() should be 'imperial'")
	}
}
