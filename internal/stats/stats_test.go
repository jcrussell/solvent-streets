package stats

import "testing"

// TestMedian: an odd count takes the middle value and an even count the
// mean of the two middle values.
func TestMedian(t *testing.T) {
	for _, tc := range []struct {
		vals []float64
		want float64
	}{
		{[]float64{5}, 5},
		{[]float64{1, 3}, 2},
		{[]float64{1, 2, 9}, 2},
		{[]float64{1, 2, 4, 9}, 3},
	} {
		if got := Median(tc.vals); got != tc.want {
			t.Errorf("Median(%v) = %v, want %v", tc.vals, got, tc.want)
		}
	}
}
