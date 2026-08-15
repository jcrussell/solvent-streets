package cmdutil

import "fmt"

// HumanSize formats a byte count as a compact human-readable string using
// binary units ("1.5 MiB"). Byte counts below 1 KiB are printed exactly,
// since rounding "900 B" to "0.9 KiB" loses more than it saves.
//
// Adapted from checksite's former private humanSize, with a fix rather
// than a straight move: that version's suffix table stopped at TiB and
// indexed it unguarded, so any input at or above 1 PiB panicked with
// index-out-of-range. checksite only ever fed it a site tree, so the bug
// was unreachable there; the HTTP cache pruner reports whatever is on
// disk, so the table is extended and the index clamped.
func HumanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	suffixes := []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	if exp >= len(suffixes) {
		exp = len(suffixes) - 1
	}
	return fmt.Sprintf("%.1f %s", float64(b)/float64(div), suffixes[exp])
}
