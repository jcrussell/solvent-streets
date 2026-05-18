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
		"Bold":   cs.Bold,
		"Green":  cs.Green,
		"Yellow": cs.Yellow,
		"Red":    cs.Red,
		"Gray":   cs.Gray,
		"Cyan":   cs.Cyan,
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			got := fn(in)
			if got != in {
				t.Fatalf("%s(%q) = %q, want identity %q", name, in, got, in)
			}
		})
	}

	if got := cs.Boldf("x=%d", 7); got != "x=7" {
		t.Errorf("Boldf disabled = %q, want identity %q", got, "x=7")
	}
	if got := cs.Greenf("ok=%v", true); got != "ok=true" {
		t.Errorf("Greenf disabled = %q, want identity %q", got, "ok=true")
	}
	if got := cs.Redf("err=%s", "boom"); got != "err=boom" {
		t.Errorf("Redf disabled = %q, want identity %q", got, "err=boom")
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
		{"Yellow", cs.Yellow, "\033[33m"},
		{"Red", cs.Red, "\033[31m"},
		{"Gray", cs.Gray, "\033[90m"},
		{"Cyan", cs.Cyan, "\033[36m"},
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

// TestColorScheme_FormattersDelegate confirms the *f variants feed through
// fmt.Sprintf before wrapping, in both enabled and disabled modes. This is
// the contract that lets call sites write cs.Redf("error: %v", err).
func TestColorScheme_FormattersDelegate(t *testing.T) {
	t.Run("enabled", func(t *testing.T) {
		cs := NewColorScheme(true)
		if got, want := cs.Boldf("n=%d", 3), "\033[1mn=3\033[0m"; got != want {
			t.Errorf("Boldf enabled = %q, want %q", got, want)
		}
		if got, want := cs.Greenf("ok"), "\033[32mok\033[0m"; got != want {
			t.Errorf("Greenf enabled = %q, want %q", got, want)
		}
		if got, want := cs.Redf("err=%s", "x"), "\033[31merr=x\033[0m"; got != want {
			t.Errorf("Redf enabled = %q, want %q", got, want)
		}
	})
	t.Run("disabled", func(t *testing.T) {
		cs := NewColorScheme(false)
		if got, want := cs.Boldf("n=%d", 3), "n=3"; got != want {
			t.Errorf("Boldf disabled = %q, want %q", got, want)
		}
	})
}
