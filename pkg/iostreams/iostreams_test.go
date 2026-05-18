package iostreams

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

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

// TestTest_PerStreamTTYFlagsDefaultFalse pins byob-iostreams.1: the three
// streams each carry an independent TTY flag, and Test() starts them all
// false. Tests that exercise interactive paths must opt in explicitly with
// the matching setter — surprise true defaults would let TTY-only code
// paths run in `go test` and produce ANSI escapes in golden output.
func TestTest_PerStreamTTYFlagsDefaultFalse(t *testing.T) {
	ios, _, _, _ := Test()
	if ios.IsStdinTTY() {
		t.Error("Test().IsStdinTTY() = true; want false")
	}
	if ios.IsStdoutTTY() {
		t.Error("Test().IsStdoutTTY() = true; want false")
	}
	if ios.IsStderrTTY() {
		t.Error("Test().IsStderrTTY() = true; want false")
	}
	if ios.IsTTY() != ios.IsStdoutTTY() {
		t.Errorf("IsTTY() = %v, want IsStdoutTTY() = %v (alias contract)",
			ios.IsTTY(), ios.IsStdoutTTY())
	}
}

// TestStreamRoutingContractDocumented locks in byob-iostreams.3: the
// IOStreams type and its Out/ErrOut field doc comments must spell out the
// data-vs-chatter routing rule. The rule is what makes the rest of the
// codebase auditable — a future author who deletes the comment loses the
// only place where "where does this print go?" is answered. We parse the
// source instead of grepping so we can match on the actual declared
// fields rather than incidental text in code or strings.
func TestStreamRoutingContractDocumented(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "iostreams.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse iostreams.go: %v", err)
	}

	var (
		typeDoc string
		outDoc  string
		errDoc  string
	)
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "IOStreams" {
			return true
		}
		if gd, ok := n.(*ast.GenDecl); ok && gd.Doc != nil {
			typeDoc = gd.Doc.Text()
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			return false
		}
		for _, f := range st.Fields.List {
			if f.Doc == nil {
				continue
			}
			for _, name := range f.Names {
				switch name.Name {
				case "Out":
					outDoc = f.Doc.Text()
				case "ErrOut":
					errDoc = f.Doc.Text()
				}
			}
		}
		return false
	})

	// The TypeSpec's GenDecl carries the doc when it's the only spec, so
	// walk the file's Decls directly to retrieve it if needed.
	if typeDoc == "" {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Doc == nil {
				continue
			}
			for _, spec := range gd.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name.Name == "IOStreams" {
					typeDoc = gd.Doc.Text()
				}
			}
		}
	}

	if !strings.Contains(typeDoc, "byob-iostreams.3") {
		t.Errorf("IOStreams doc comment should reference byob-iostreams.3 to anchor the routing contract; got:\n%s", typeDoc)
	}
	for _, kw := range []string{"DATA", "chatter"} {
		if !strings.Contains(typeDoc, kw) {
			t.Errorf("IOStreams doc comment missing %q; the routing rule must be stated on the type", kw)
		}
	}

	if outDoc == "" {
		t.Error("IOStreams.Out has no doc comment; byob-iostreams.3 requires a routing note on the field")
	} else if !strings.Contains(outDoc, "DATA") {
		t.Errorf("IOStreams.Out doc must call out DATA; got:\n%s", outDoc)
	}
	if errDoc == "" {
		t.Error("IOStreams.ErrOut has no doc comment; byob-iostreams.3 requires a routing note on the field")
	} else if !strings.Contains(errDoc, "chatter") {
		t.Errorf("IOStreams.ErrOut doc must call out chatter; got:\n%s", errDoc)
	}
}

// TestSetters_TouchOnlyTheirOwnStream pins that each Set*TTY setter mutates
// exactly one flag. A regression that aliased two flags would silently
// activate prompt or progress code paths in tests that only meant to flip
// stdout — and the resulting failures would be far from the cause.
func TestSetters_TouchOnlyTheirOwnStream(t *testing.T) {
	t.Run("SetTTY only stdout", func(t *testing.T) {
		ios, _, _, _ := Test()
		ios.SetTTY(true)
		if !ios.IsStdoutTTY() {
			t.Error("after SetTTY(true), IsStdoutTTY() = false")
		}
		if ios.IsStdinTTY() || ios.IsStderrTTY() {
			t.Errorf("SetTTY leaked: stdin=%v stderr=%v", ios.IsStdinTTY(), ios.IsStderrTTY())
		}
	})
	t.Run("SetStdinTTY only stdin", func(t *testing.T) {
		ios, _, _, _ := Test()
		ios.SetStdinTTY(true)
		if !ios.IsStdinTTY() {
			t.Error("after SetStdinTTY(true), IsStdinTTY() = false")
		}
		if ios.IsStdoutTTY() || ios.IsStderrTTY() {
			t.Errorf("SetStdinTTY leaked: stdout=%v stderr=%v", ios.IsStdoutTTY(), ios.IsStderrTTY())
		}
	})
	t.Run("SetStderrTTY only stderr", func(t *testing.T) {
		ios, _, _, _ := Test()
		ios.SetStderrTTY(true)
		if !ios.IsStderrTTY() {
			t.Error("after SetStderrTTY(true), IsStderrTTY() = false")
		}
		if ios.IsStdoutTTY() || ios.IsStdinTTY() {
			t.Errorf("SetStderrTTY leaked: stdout=%v stdin=%v", ios.IsStdoutTTY(), ios.IsStdinTTY())
		}
	})
}
