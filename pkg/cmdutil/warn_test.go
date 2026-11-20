package cmdutil_test

import (
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/warn"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// Warnf and warn.Fprintf write the same line: one prefix for every warning,
// whether the caller holds the IOStreams or only the stderr writer.
func TestWarnf(t *testing.T) {
	ios, _, out, errOut := iostreams.Test()
	cmdutil.Warnf(ios, "%s fetch failed: %v", "overpass", "timeout")
	var b strings.Builder
	warn.Fprintf(&b, "%s fetch failed: %v", "overpass", "timeout")

	const want = "warning: overpass fetch failed: timeout\n"
	if errOut.String() != want || b.String() != want {
		t.Errorf("Warnf wrote %q, warn.Fprintf wrote %q; want both %q", errOut.String(), b.String(), want)
	}
	if out.Len() != 0 {
		t.Errorf("a warning must not reach stdout, got %q", out.String())
	}
}
