package cmdutil

import (
	"strings"
	"testing"
)

func TestReflow(t *testing.T) {
	in := "one two three four five six seven\neight\n\n  - kept as\n    written\n\nnine ten"
	got := Reflow(in, 14)
	want := "one two three\nfour five six\nseven eight\n\n  - kept as\n    written\n\nnine ten"
	if got != want {
		t.Errorf("Reflow =\n%s\nwant\n%s", got, want)
	}
	if got := Reflow("run 'pvmt snapshots create <name>' next", 20); got != "run\n'pvmt snapshots create <name>'\nnext" {
		t.Errorf("Reflow split a quoted command:\n%s", got)
	}
	for line := range strings.SplitSeq(Reflow(strings.Repeat("word ", 40), HelpWidth), "\n") {
		if len(line) > HelpWidth {
			t.Errorf("line %q is longer than %d", line, HelpWidth)
		}
	}
}
