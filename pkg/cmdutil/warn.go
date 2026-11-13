package cmdutil

import (
	"fmt"
	"io"

	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// Warnf prints a non-fatal warning to ios.ErrOut with a consistent
// "warning: " prefix and a trailing newline. Use it for partial-failure
// notices that should reach the user without aborting the command —
// errors that should abort use ErrHint/FlagErrorf instead.
//
// Format messages in Go stdlib style: lowercase leading letter, no
// trailing punctuation. The "warning: " prefix is supplied here.
func Warnf(ios *iostreams.IOStreams, format string, args ...any) {
	FprintWarnf(ios.ErrOut, format, args...)
}

// FprintWarnf is Warnf for a caller that holds only the stderr writer (a
// producer handed errOut, or the TUI's log sink).
func FprintWarnf(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, "warning: "+format+"\n", args...)
}
