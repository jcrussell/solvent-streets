package cmdutil

import (
	"errors"
	"fmt"
)

// ErrSilent is returned when the error message has already been printed
// and the command should exit with a non-zero exit code without printing anything else.
var ErrSilent = errors.New("silent error")

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

// ErrPending is returned when a command completes without error but work is still pending.
var ErrPending = errors.New("pending")
