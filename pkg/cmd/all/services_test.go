package all

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestRunOptionalStep pins the best-effort contract of the per-hex metric
// steps in `pvmt all` (fire, census): an empty result is a silent skip,
// cancellation aborts, and any other failure is warned about but never fails
// the run.
func TestRunOptionalStep(t *testing.T) {
	ios, _, _, errOut := iostreams.Test()
	if err := runOptionalStep(ios, "fire ingest", func() error { return cmdutil.ErrNoResults }); err != nil {
		t.Errorf("ErrNoResults must be a silent skip, got %v", err)
	}
	if err := runOptionalStep(ios, "fire ingest", func() error { return context.Canceled }); !errors.Is(err, context.Canceled) {
		t.Errorf("cancellation must propagate, got %v", err)
	}
	if err := runOptionalStep(ios, "fire compute", func() error { return errors.New("overpass down") }); err != nil {
		t.Errorf("other failures must be dropped after a warning, got %v", err)
	}
	if !strings.Contains(errOut.String(), "warning: fire compute failed: overpass down") {
		t.Errorf("expected a warning on ErrOut, got %q", errOut.String())
	}
}

// TestRunOptionalSteps: every step runs and the pavement result stands,
// unless a step is cancelled — then the remaining steps are skipped and the
// cancellation wins.
func TestRunOptionalSteps(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	var ran []string
	step := func(name string, err error) optionalStep {
		return optionalStep{name, func() error { ran = append(ran, name); return err }}
	}
	got := runOptionalSteps(cmdutil.ErrNoResults, ios, []optionalStep{step("a", errors.New("boom")), step("b", nil)})
	if !errors.Is(got, cmdutil.ErrNoResults) || strings.Join(ran, ",") != "a,b" {
		t.Errorf("result %v, ran %v; want the pavement result and both steps", got, ran)
	}
	ran = nil
	got = runOptionalSteps(nil, ios, []optionalStep{step("a", context.Canceled), step("b", nil)})
	if !errors.Is(got, context.Canceled) || strings.Join(ran, ",") != "a" {
		t.Errorf("result %v, ran %v; want cancellation and no further steps", got, ran)
	}
}
