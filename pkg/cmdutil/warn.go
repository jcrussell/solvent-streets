package cmdutil

import (
	"io"

	"github.com/jcrussell/solvent-streets/internal/warn"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// Warnf prints a non-fatal warning to ios.ErrOut with a consistent
// "warning: " prefix and a trailing newline. Use it for partial-failure
// notices that should reach the user without aborting the command —
// errors that should abort use ErrHint/FlagErrorf instead.
//
// Format messages in Go stdlib style: lowercase leading letter, no
// trailing punctuation. The "warning: " prefix is supplied here. A caller
// that holds only the stderr writer uses warn.Fprintf, which writes the same
// line.
func Warnf(ios *iostreams.IOStreams, format string, args ...any) {
	warn.Fprintf(ios.ErrOut, format, args...)
}

// SkipNeedsFirst warns that err leaves the command nothing to do until
// command has run (NeedsFirst), and returns ErrNoResults: a quiet skip in a
// fan-out over cities or steps, exit 3 on its own.
func SkipNeedsFirst(w io.Writer, err error, command string) error {
	warn.Fprint(w, NeedsFirst(err, command))
	return ErrNoResults
}
