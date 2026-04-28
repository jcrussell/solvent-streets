package cmdutil

import (
	"errors"
	"fmt"
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

// Hintf wraps err with a formatted remediation hint. Returns nil if err is nil.
func Hintf(err error, format string, a ...any) error {
	if err == nil {
		return nil
	}
	return &ErrHint{Err: err, Hint: fmt.Sprintf(format, a...)}
}

// ErrCancel is returned when the user cancels an operation.
var ErrCancel = errors.New("cancel")

// FlagError indicates a user error with command flags.
type FlagError struct {
	Err error
}

func (e *FlagError) Error() string {
	return e.Err.Error()
}

func (e *FlagError) Unwrap() error {
	return e.Err
}

func FlagErrorf(format string, args ...any) error {
	return &FlagError{Err: fmt.Errorf(format, args...)}
}

// ErrNoResults is returned when a command produces no results.
// The command should print a contextual message to stderr before returning this.
var ErrNoResults = errors.New("no results")
