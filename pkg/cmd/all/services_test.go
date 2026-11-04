package all

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestRunServiceStep pins the best-effort contract of the fire steps in
// `pvmt all`: an empty result is a silent skip, cancellation aborts, and any
// other failure is warned about but never fails the run.
func TestRunServiceStep(t *testing.T) {
	ios, _, _, errOut := iostreams.Test()
	if err := runServiceStep(ios, "ingest", func() error { return cmdutil.ErrNoResults }); err != nil {
		t.Errorf("ErrNoResults must be a silent skip, got %v", err)
	}
	if err := runServiceStep(ios, "ingest", func() error { return context.Canceled }); !errors.Is(err, context.Canceled) {
		t.Errorf("cancellation must propagate, got %v", err)
	}
	if err := runServiceStep(ios, "compute", func() error { return errors.New("overpass down") }); err != nil {
		t.Errorf("other failures must be dropped after a warning, got %v", err)
	}
	if !strings.Contains(errOut.String(), "warning: fire compute failed: overpass down") {
		t.Errorf("expected a warning on ErrOut, got %q", errOut.String())
	}
	if got := joinServiceStep(cmdutil.ErrNoResults, nil); !errors.Is(got, cmdutil.ErrNoResults) {
		t.Errorf("pavement result must stand when the step is fine, got %v", got)
	}
	if got := joinServiceStep(nil, context.Canceled); !errors.Is(got, context.Canceled) {
		t.Errorf("a cancelled step must win, got %v", got)
	}
}
