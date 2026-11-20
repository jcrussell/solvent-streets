// Package stats holds small, dependency-free numeric helpers shared by the
// exporter, check-site and the game.
package stats

// Median is the middle of sorted vals, or the mean of the two middle values
// for an even count. sorted must be non-empty.
func Median(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
