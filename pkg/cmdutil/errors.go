package cmdutil

import (
	"errors"
	"fmt"
)

// ErrSilent is returned when the error message has already been printed
// and the command should exit with a non-zero exit code without printing anything else.
var ErrSilent = errors.New("silent error")

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
