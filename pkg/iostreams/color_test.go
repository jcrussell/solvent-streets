package iostreams

import (
	"strings"
	"testing"
)

// TestColorScheme_DisabledIsIdentity pins the core contract of byob-iostreams.2:
// when colors are off, every method returns its input verbatim — no ANSI
// escapes, no length change. Call sites can therefore wrap output in
// cs.Red(...) etc. without guarding on IsColorEnabled.
func TestColorScheme_DisabledIsIdentity(t *testing.T) {
	cs := NewColorScheme(false)
	const in = "hello"

	cases := map[string]func(string) string{
		"Bold":  cs.Bold,
		"Green": cs.Green,
		"Red":   cs.Red,
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			got := fn(in)
			if got != in {
				t.Fatalf("%s(%q) = %q, want identity %q", name, in, got, in)
			}
		})
	}
}

// TestColorScheme_EnabledWrapsWithANSI pins that each method emits the
// correct ANSI SGR code and is properly terminated with the reset sequence
// when colors are on. A future refactor that swaps codes or drops the
// reset will fail here.
func TestColorScheme_EnabledWrapsWithANSI(t *testing.T) {
	cs := NewColorScheme(true)
	const in = "hello"
	const reset = "\033[0m"

	cases := []struct {
		name string
		fn   func(string) string
		code string
	}{
		{"Bold", cs.Bold, "\033[1m"},
		{"Green", cs.Green, "\033[32m"},
		{"Red", cs.Red, "\033[31m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.fn(in)
			want := tc.code + in + reset
			if got != want {
				t.Fatalf("%s(%q) = %q, want %q", tc.name, in, got, want)
			}
			if !strings.HasSuffix(got, reset) {
				t.Errorf("%s output missing reset suffix: %q", tc.name, got)
			}
		})
	}
}
