package cmdutil

import "fmt"

// HumanSize formats a byte count as a compact human-readable string using
// binary units ("1.5 MiB"). Byte counts below 1 KiB are printed exactly,
// since rounding "900 B" to "0.9 KiB" loses more than it saves.
//
// The suffix table runs past TiB and the index into it is clamped: the HTTP
// cache pruner reports whatever is on disk, so an input at or above 1 PiB
// must not index past the table and panic.
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
