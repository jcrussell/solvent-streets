// Package textutil holds small, dependency-free string helpers shared
// across packages that would otherwise each hand-roll (and mis-handle)
// UTF-8-aware slicing.
package textutil

import "unicode/utf8"

// TruncateRunes returns s unchanged when it contains at most maxRunes
// runes; otherwise it returns the first maxRunes runes followed by
// ellipsis. It slices on rune boundaries, so it never splits a multi-byte
// UTF-8 rune (which would corrupt the string with a replacement char).
// maxRunes < 0 is treated as 0.
func TruncateRunes(s string, maxRunes int, ellipsis string) string {
	if maxRunes < 0 {
		maxRunes = 0
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	// Walk maxRunes runes forward to find the byte offset of the cut so the
	// slice lands on a rune boundary.
	i, count := 0, 0
	for i < len(s) && count < maxRunes {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		count++
	}
	return s[:i] + ellipsis
}

// Plural returns one when n is 1 and many otherwise, for a count printed
// beside its noun: fmt.Sprintf("%d %s", n, Plural(n, "city", "cities")).
func Plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
