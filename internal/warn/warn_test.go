package warn

import (
	"errors"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/hint"
)

func TestFprint(t *testing.T) {
	var b strings.Builder
	Fprintf(&b, "%s failed", "overpass")
	Fprint(&b, hint.With(errors.New("no boundary stored"), "run 'pvmt all ingest' first"))
	const want = "warning: overpass failed\nwarning: no boundary stored\nhint: run 'pvmt all ingest' first\n"
	if b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
}
