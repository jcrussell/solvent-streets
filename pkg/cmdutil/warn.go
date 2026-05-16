package cmdutil

import (
	"fmt"

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
	fmt.Fprintf(ios.ErrOut, "warning: "+format+"\n", args...)
}
