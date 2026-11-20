// Package msgvoice_test keeps every user-facing error, warning and hint in
// one voice, the one CONTRIBUTING.md's command-line conventions describe: a
// Go-style message (no leading capital, no trailing period), the fix in a
// hint rather than after a dash, commands quoted 'pvmt like this', and the
// "warning: " prefix written only by internal/warn.
package msgvoice_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// messageArg maps a call (package-qualified, or bare inside its own package)
// to the index of its message argument.
var messageArg = map[string]int{
	"fmt.Errorf":          0,
	"errors.New":          0,
	"FlagErrorf":          0,
	"cmdutil.FlagErrorf":  0,
	"hint.With":           1,
	"hint.Withf":          1,
	"hint.Addf":           1,
	"cmdutil.Warnf":       1,
	"cmdutil.FprintWarnf": 1,
	"Warnf":               1,
	"FprintWarnf":         1,
	"warn.Fprintf":        1,
	"warnpkg.Fprintf":     1,
	// The command a missing prerequisite names, and the noun an unknown
	// value is reported for.
	"NeedsFirst":                 1,
	"cmdutil.NeedsFirst":         1,
	"hint.NeedsFirst":            1,
	"SkipNeedsFirst":             2,
	"cmdutil.SkipNeedsFirst":     2,
	"UnknownValueErrorf":         0,
	"cmdutil.UnknownValueErrorf": 0,
}

// properNouns may start a message: names that are capitalized mid-sentence.
var properNouns = []string{
	"ArcGIS", "Census", "GeoJSON", "JSON", "Nominatim", "OSM", "OpenStreetMap",
	"Overpass", "SQLite", "TIGERweb", "UTM", "WASM", "PVMT_", "HTTP", "URL", "NFPA",
}

func TestMessageVoice(t *testing.T) {
	root := filepath.Join("..", "..")
	var problems []string
	for _, dir := range []string{"internal", "pkg", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "testdata" {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			problems = append(problems, checkFile(t, path)...)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range problems {
		t.Error(p)
	}
}

func checkFile(t *testing.T, path string) []string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	ownWarn := strings.Contains(filepath.ToSlash(path), "internal/warn/")
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			i, ok := messageArg[callName(n.Fun)]
			if !ok || i >= len(n.Args) {
				return true
			}
			if msg, ok := literal(n.Args[i]); ok {
				if p := checkMessage(msg); p != "" {
					out = append(out, fset.Position(n.Pos()).String()+": "+p+": "+strconv.Quote(msg))
				}
			}
		case *ast.BasicLit:
			if ownWarn || n.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(n.Value)
			if err == nil && (strings.HasPrefix(s, "warning: ") || strings.HasPrefix(s, "note: ")) {
				out = append(out, fset.Position(n.Pos()).String()+": writes its own warning prefix; use cmdutil.Warnf or warn.Fprintf")
			}
		}
		return true
	})
	return out
}

func callName(fun ast.Expr) string {
	switch fn := fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		if x, ok := fn.X.(*ast.Ident); ok {
			return x.Name + "." + fn.Sel.Name
		}
	}
	return ""
}

// literal is the text of a string literal, or of the leading literals of a
// "a" + "b" concatenation.
func literal(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		return s, err == nil
	case *ast.BinaryExpr:
		l, lok := literal(e.X)
		r, rok := literal(e.Y)
		switch {
		case lok && rok:
			return l + r, true
		case lok:
			return l, true
		}
	}
	return "", false
}

func checkMessage(msg string) string {
	trimmed := strings.TrimSpace(msg)
	if trimmed == "" {
		return ""
	}
	// A message that starts with a verb (%s, %w) has no first letter of its
	// own to check; the other rules still apply.
	if r, _ := utf8.DecodeRuneInString(trimmed); unicode.IsUpper(r) && !startsWithProperNoun(trimmed) {
		return "starts with a capital"
	}
	if strings.HasSuffix(trimmed, ".") && !strings.HasSuffix(trimmed, "...") {
		return "ends with a period"
	}
	if strings.Contains(msg, " — run ") {
		return "puts its fix after a dash; attach it with hint.Withf or cmdutil.NeedsFirst"
	}
	if strings.Contains(msg, "`") {
		return "quotes in backticks; quote a command 'pvmt ...' and a value with %q"
	}
	if retryAside.MatchString(msg) {
		return "puts its fix in parentheses; attach it with hint.Withf"
	}
	return ""
}

// retryAside is a fix tucked into the message: "(retry: pvmt ...)".
var retryAside = regexp.MustCompile(`\((retry|run|try): *'?pvmt `)

func startsWithProperNoun(s string) bool {
	for _, p := range properNouns {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
