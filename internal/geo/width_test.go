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
