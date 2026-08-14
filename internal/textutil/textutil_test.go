package textutil

import (
	"testing"
	"unicode/utf8"
)

func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		maxRunes int
		ellipsis string
		want     string
	}{
		{name: "short ascii unchanged", in: "hello", maxRunes: 10, ellipsis: "…", want: "hello"},
		{name: "exact length unchanged", in: "hello", maxRunes: 5, ellipsis: "…", want: "hello"},
		{name: "ascii truncated", in: "hello world", maxRunes: 5, ellipsis: "...", want: "hello..."},
		// Each emoji is a 4-byte rune; a byte-index cut at 5 would split one.
		{name: "multibyte not split", in: "😀😀😀", maxRunes: 2, ellipsis: "…", want: "😀😀…"},
		{name: "multibyte unchanged", in: "café", maxRunes: 4, ellipsis: "…", want: "café"},
		{name: "negative max", in: "abc", maxRunes: -1, ellipsis: "…", want: "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TruncateRunes(tc.in, tc.maxRunes, tc.ellipsis)
			if got != tc.want {
				t.Errorf("TruncateRunes(%q, %d) = %q; want %q", tc.in, tc.maxRunes, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("TruncateRunes produced invalid UTF-8: %q", got)
			}
		})
	}
}
