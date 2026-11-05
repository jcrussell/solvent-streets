package cmdutil

import (
	"errors"
	"fmt"
)

// ErrCancel is returned when the user cancels an operation.
var ErrCancel = errors.New("cancel")

// ErrSilent signals that the command already reported its failure to the
// user (typically a contextual diagnostic on stderr) and the top-level
// runner should exit non-zero without printing the error itself. Use this
// when the natural error message would be redundant — e.g. a subcommand
// has already streamed per-item failure lines and a final "Error: 3 of 5
// failed" would just repeat what the user saw.
var ErrSilent = errors.New("silent")

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

// ErrAllSourcesFailed signals that every upstream source for a resource
// errored — distinct from ErrNoResults (sources succeeded but returned
// zero features). Used by ingest to surface deployment-wide outages
// that would otherwise look like a silent no-op via `pvmt all`.
var ErrAllSourcesFailed = errors.New("all sources failed")
