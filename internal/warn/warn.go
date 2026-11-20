// Package warn writes non-fatal warnings in the one format every pvmt
// command uses: "warning: " and a Go-style message (lowercase, no trailing
// period), with the remediation hint of an error, if it carries one, on the
// line below. It is a leaf, importing only the standard library and
// internal/hint, so domain packages that hold only a stderr writer use the
// same format as the CLI layer's cmdutil.Warnf.
package warn

import (
	"fmt"
	"io"

	"github.com/jcrussell/solvent-streets/internal/hint"
)

// Prefix starts every warning line.
const Prefix = "warning: "

// Fprintf writes one warning line to w.
func Fprintf(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, Prefix+format+"\n", args...)
}

// Fprint writes err as a warning, followed by its hint when it carries one.
func Fprint(w io.Writer, err error) {
	Fprintf(w, "%v", err)
	hint.Fprint(w, err)
}
