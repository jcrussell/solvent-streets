// Package hint attaches actionable remediation text to an error. It is a
// leaf — it imports only the standard library — so domain packages can hint at their failure
// sites without importing the CLI layer. The
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

// Addf attaches another hint to err after the ones it carries: they all
// print together, the carried ones first, so a caller adding a retry command
// does not hide the cause's own hints (one per branch of a join). Returns
// nil if err is nil.
func Addf(err error, format string, a ...any) error {
	if err == nil {
		return nil
	}
	return With(err, strings.Join(append(Hints(err), fmt.Sprintf(format, a...)), "\n"))
}

// Fprint writes err's hints to w, each as "hint: " and its first line, then
// each further line indented to align under it. It is how every command
// shows a hint, below the error or warning it belongs to. A joined error (a
// run over several cities) prints every distinct hint of its branches, in
// order, not only the first.
func Fprint(w io.Writer, err error) {
	for _, h := range Hints(err) {
		lines := strings.Split(h, "\n")
		fmt.Fprintf(w, "hint: %s\n", lines[0])
		for _, l := range lines[1:] {
			fmt.Fprintf(w, "      %s\n", l)
		}
	}
}

// Hints returns the distinct hints err carries, in order: the outermost
// ErrHint on each branch of the error tree (errors.As's pick along a chain),
// one per branch of a join.
func Hints(err error) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(error)
	walk = func(err error) {
		for err != nil {
			if he, ok := err.(*ErrHint); ok { //nolint:errorlint // the walk unwraps itself
				if he.Hint != "" && !seen[he.Hint] {
					seen[he.Hint] = true
					out = append(out, he.Hint)
				}
				return
			}
			if j, ok := err.(interface{ Unwrap() []error }); ok {
				for _, branch := range j.Unwrap() {
					walk(branch)
				}
				return
			}
			err = errors.Unwrap(err)
		}
	}
	walk(err)
	return out
}

// PrereqError is a command that cannot run until another one has: nothing
// is ingested, computed or exported yet. The CLI exits 3 for it, as for an
// empty result, and prints it with its hint.
type PrereqError struct {
	Err error
}

func (e *PrereqError) Error() string { return e.Err.Error() }
func (e *PrereqError) Unwrap() error { return e.Err }

// NeedsFirst marks err as a missing prerequisite that command provides, with
// the hint "run '<command>' first".
func NeedsFirst(err error, command string) error {
	if err == nil {
		return nil
	}
	return Withf(&PrereqError{Err: err}, "run '%s' first", command)
}
