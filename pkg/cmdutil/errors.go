package cmdutil

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jcrussell/solvent-streets/internal/hint"
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

// ErrPartialIngest signals that some of a resource's sources failed while
// others answered: what answered was saved, but the resource is incomplete
// for this run. It fails the run (exit 1) so a chained
// `ingest && compute && export` does not publish a city missing a source
// as if it were complete.
var ErrPartialIngest = errors.New("some sources failed")

// NeedsFirst reports that err stops the command until command has run
// (hint.NeedsFirst): it exits 3 and prints with the hint
// "run '<command>' first", and a fan-out over cities collects it like any
// failure.
func NeedsFirst(err error, command string) error { return hint.NeedsFirst(err, command) }

// OnlyPrereqs reports whether err stopped only for want of an earlier step:
// it is a hint.PrereqError, or wraps one, or joins errors that all are. A
// run in which some item still produced a result (partialError) is not.
func OnlyPrereqs(err error) bool {
	// A type switch, not errors.As: the walk itself unwraps one layer at a
	// time so that it can see a join (or a partialError) wherever it sits.
	switch e := err.(type) { //nolint:errorlint // see above
	case nil:
		return false
	case *partialError:
		return false
	case *hint.PrereqError:
		return true
	case interface{ Unwrap() []error }:
		for _, branch := range e.Unwrap() {
			if !OnlyPrereqs(branch) {
				return false
			}
		}
		return true
	}
	return OnlyPrereqs(errors.Unwrap(err))
}

// partialError is a fan-out's failure when other items produced a result:
// it prints and hints as err does, but exits 1 even when every failure is a
// missing prerequisite, since the run did work.
type partialError struct{ err error }

func (e *partialError) Error() string { return e.err.Error() }
func (e *partialError) Unwrap() error { return e.err }

// UnknownValueErrorf is the usage error for a flag or argument value that is
// not one of valid: `unknown <what> "<value>" (valid: a, b)`.
func UnknownValueErrorf(what, value string, valid []string) error {
	return FlagErrorf("unknown %s %q (valid: %s)", what, value, strings.Join(valid, ", "))
}
