package cmdutil_test

import (
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// Warnf and FprintWarnf write the same line: one prefix for every warning,
// whether the caller holds the IOStreams or only the stderr writer.
func TestWarnf(t *testing.T) {
	ios, _, out, errOut := iostreams.Test()
	cmdutil.Warnf(ios, "%s fetch failed: %v", "overpass", "timeout")
	var b strings.Builder
	cmdutil.FprintWarnf(&b, "%s fetch failed: %v", "overpass", "timeout")

	const want = "warning: overpass fetch failed: timeout\n"
	if errOut.String() != want || b.String() != want {
		t.Errorf("Warnf wrote %q, FprintWarnf wrote %q; want both %q", errOut.String(), b.String(), want)
	}
	if out.Len() != 0 {
		t.Errorf("a warning must not reach stdout, got %q", out.String())
	}
}
