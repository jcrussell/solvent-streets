package geo

import (
	"math"
	"testing"
)

func TestInferWidth(t *testing.T) {
	tests := []struct {
		name     string
		tags     map[string]string
		expected float64
	}{
		{"explicit width", map[string]string{"width": "12.5"}, 12.5},
		{"explicit width ignores parking", map[string]string{"width": "12", "parking:left": "lane"}, 12.0},
		// E2: unit-suffixed widths win over lanes/classification and never
		// get the parking addon (surveyed widths already account for it).
		{"metres suffix wins over parking", map[string]string{"width": "5 m", "parking:left": "lane"}, 5.0},
		{"feet suffix wins over lanes", map[string]string{"width": "12 ft", "lanes": "4"}, 12 * 0.3048},
		{"feet apostrophe", map[string]string{"width": "16'"}, 16 * 0.3048},
		{"lanes based", map[string]string{"lanes": "4"}, 14.8},
		{"motorway", map[string]string{"highway": "motorway"}, 14.0},
		{"residential", map[string]string{"highway": "residential"}, 5.5},
		{"residential with parking", map[string]string{"highway": "residential", "parking:left": "lane", "parking:right": "lane"}, 5.5 + 4.8},
		{"unknown highway", map[string]string{"highway": "unknown_type"}, 5.5},
		{"no tags", map[string]string{}, 5.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InferWidth(tt.tags)
			if math.Abs(got-tt.expected) > 0.01 {
				t.Errorf("InferWidth(%v) = %f, want %f", tt.tags, got, tt.expected)
			}
		})
	}
}

func TestParseOSMWidth(t *testing.T) {
	const ft = 0.3048
	const in = 0.0254
	tests := []struct {
		name string
		in   string
		want float64
		ok   bool
	}{
		{"bare metres", "5", 5.0, true},
		{"bare decimal metres", "5.5", 5.5, true},
		{"metres suffix spaced", "5 m", 5.0, true},
		{"metres suffix tight", "5m", 5.0, true},
		{"feet ft spaced", "12 ft", 12 * ft, true},
		{"feet ft tight", "12ft", 12 * ft, true},
		{"feet word", "12 feet", 12 * ft, true},
		{"feet apostrophe", "16'", 16 * ft, true},
		{"feet and inches", "3'6\"", 3*ft + 6*in, true},
		{"feet and inches no closing quote", "3'6", 3*ft + 6*in, true},
		{"inches quote", "16\"", 16 * in, true},
		{"inches in suffix", "16 in", 16 * in, true},
		{"surrounding whitespace", "  7.0  ", 7.0, true},
		{"empty", "", 0, false},
		{"non-numeric", "wide", 0, false},
		{"zero", "0", 0, false},
		{"negative", "-3", 0, false},
		{"garbage suffix", "5 yards", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseOSMWidth(tt.in)
			if ok != tt.ok {
				t.Fatalf("parseOSMWidth(%q) ok = %v, want %v", tt.in, ok, tt.ok)
			}
			if ok && math.Abs(got-tt.want) > 0.0001 {
				t.Errorf("parseOSMWidth(%q) = %f, want %f", tt.in, got, tt.want)
			}
		})
	}
}

func TestInferSidewalkWidth_UnitSuffix(t *testing.T) {
	tests := []struct {
		name string
		tags map[string]string
		want float64
	}{
		{"bare metres", map[string]string{"width": "2.5"}, 2.5},
		{"metres suffix", map[string]string{"width": "2 m"}, 2.0},
		{"feet suffix beats classification", map[string]string{"width": "6 ft", "highway": "footway"}, 6 * 0.3048},
		{"unparseable falls back to classification", map[string]string{"width": "wide", "highway": "footway"}, 1.5},
		{"classification fallback", map[string]string{"highway": "pedestrian"}, 3.0},
		{"ultimate fallback", map[string]string{}, 1.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InferSidewalkWidth(tt.tags)
			if math.Abs(got-tt.want) > 0.01 {
				t.Errorf("InferSidewalkWidth(%v) = %f, want %f", tt.tags, got, tt.want)
			}
		})
	}
}
