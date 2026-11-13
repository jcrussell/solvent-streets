// Package hint attaches actionable remediation text to an error. It is a
// leaf — it imports only the standard library — so domain packages can hint at their failure
// sites without importing the CLI layer (byob-errors.2, byob-layout.1). The
// top-level runner finds the hint with errors.As and prints it on the line
// after the error.
package hint

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrHint wraps an error with actionable remediation text. The runner prints
// the underlying error followed by the hint (prefixed with "hint:"), one line
// below. Multi-line hints are printed with subsequent lines indented to align
// with the first line. Keep the wrapped error's message in Go stdlib style
// (lowercase, no trailing punctuation) — the multi-line prose belongs here.
type ErrHint struct {
	Err  error
	Hint string
}

func (e *ErrHint) Error() string { return e.Err.Error() }
func (e *ErrHint) Unwrap() error { return e.Err }

// With wraps err with a remediation hint. Returns nil if err is nil, so it
// composes at the return site: `return hint.With(load(), "run pvmt ...")`.
func With(err error, h string) error {
	if err == nil {
		return nil
	}
	return &ErrHint{Err: err, Hint: h}
}

// Withf wraps err with a formatted remediation hint. Returns nil if err is nil.
func Withf(err error, format string, a ...any) error {
	if err == nil {
		return nil
	}
	return With(err, fmt.Sprintf(format, a...))
}

// Fprint writes err's hint to w, if it carries one: "hint: " and the first
// line, then each further line indented to align under it. It is how every
// command shows a hint, below the error or warning it belongs to.
func Fprint(w io.Writer, err error) {
	var he *ErrHint
	if !errors.As(err, &he) || he.Hint == "" {
		return
	}
	lines := strings.Split(he.Hint, "\n")
	fmt.Fprintf(w, "hint: %s\n", lines[0])
	for _, l := range lines[1:] {
		fmt.Fprintf(w, "      %s\n", l)
	}
}
