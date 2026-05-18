package iostreams

import "testing"

// TestShouldEnableColor pins the (isTTY, NO_COLOR) decision matrix for
// byob-iostreams.2. NO_COLOR must disable color whenever it is present —
// including the empty string — per https://no-color.org. Non-TTY always
// short-circuits regardless of NO_COLOR.
func TestShouldEnableColor(t *testing.T) {
	cases := []struct {
		name  string
		isTTY bool
		env   map[string]string
		want  bool
	}{
		{"tty + no env", true, nil, true},
		{"tty + NO_COLOR=1", true, map[string]string{"NO_COLOR": "1"}, false},
		{"tty + NO_COLOR= (empty, present)", true, map[string]string{"NO_COLOR": ""}, false},
		{"non-tty + no env", false, nil, false},
		{"non-tty + NO_COLOR=1", false, map[string]string{"NO_COLOR": "1"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(k string) (string, bool) {
				v, ok := tc.env[k]
				return v, ok
			}
			if got := shouldEnableColor(tc.isTTY, lookup); got != tc.want {
				t.Errorf("shouldEnableColor(isTTY=%v, env=%v) = %v, want %v",
					tc.isTTY, tc.env, got, tc.want)
			}
		})
	}
}

// TestTest_NoColorByDefault confirms the Test() constructor — which most of
// the suite uses — never emits color. Tests that compare output by-string
// rely on this to avoid ANSI escapes leaking into golden strings.
func TestTest_NoColorByDefault(t *testing.T) {
	ios, _, _, _ := Test()
	if ios.IsTTY() {
		t.Error("Test() IOStreams reports IsTTY=true; want false")
	}
	if ios.IsColorEnabled() {
		t.Error("Test() IOStreams reports IsColorEnabled=true; want false")
	}
	cs := ios.ColorScheme()
	if cs == nil {
		t.Fatal("Test() ColorScheme() returned nil")
	}
	const in = "hello"
	if got := cs.Red(in); got != in {
		t.Errorf("Test().ColorScheme().Red(%q) = %q, want identity", in, got)
	}
}

// TestColorScheme_LazyAndStable confirms the lazy-allocated ColorScheme on
// IOStreams is cached: repeat calls return the same instance, so callers can
// stash the pointer without worrying about it being replaced underneath them.
func TestColorScheme_LazyAndStable(t *testing.T) {
	ios, _, _, _ := Test()
	first := ios.ColorScheme()
	second := ios.ColorScheme()
	if first != second {
		t.Errorf("ColorScheme() returned different pointers across calls: %p vs %p", first, second)
	}
}
