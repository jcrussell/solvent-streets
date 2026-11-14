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
// cancellation aborts, and any other failure is warned about and returned for
// runOptionalSteps to drop (best-effort) or keep (required).
func TestRunOptionalStep(t *testing.T) {
	ios, _, _, errOut := iostreams.Test()
	if err := runOptionalStep(ios, "fire ingest", func() error { return cmdutil.ErrNoResults }); err != nil {
		t.Errorf("ErrNoResults must be a silent skip, got %v", err)
	}
	if err := runOptionalStep(ios, "fire ingest", func() error { return context.Canceled }); !errors.Is(err, context.Canceled) {
		t.Errorf("cancellation must propagate, got %v", err)
	}
	if err := runOptionalStep(ios, "fire compute", func() error { return errors.New("overpass down") }); err == nil || err.Error() != "overpass down" {
		t.Errorf("other failures must be warned about and returned, got %v", err)
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
		return optionalStep{label: name, fn: func() error { ran = append(ran, name); return err }}
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

// TestRunOptionalSteps_RequiredFailureFailsTheRun pins solvent-streets-r0ro:
// in `all ingest` the fire and Census steps are required. A failure there
// still lets the remaining steps run, but it is joined into the result, so
// the run exits 1 instead of 0.
func TestRunOptionalSteps_RequiredFailureFailsTheRun(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	var ran []string
	boom := errors.New("http2: timeout awaiting response headers")
	steps := []optionalStep{
		{label: "fire ingest", required: true, fn: func() error { ran = append(ran, "fire"); return boom }},
		{label: "census ingest", required: true, fn: func() error { ran = append(ran, "census"); return nil }},
	}
	got := runOptionalSteps(nil, ios, steps)
	if !errors.Is(got, boom) || !strings.Contains(got.Error(), "fire ingest") {
		t.Errorf("result %v; want the fire failure, labelled", got)
	}
	if strings.Join(ran, ",") != "fire,census" {
		t.Errorf("ran %v; want every step to run past the failure", ran)
	}
	// An empty pavement result must not swallow a required failure: callers
	// match ErrNoResults first and would file the city as empty
	// (solvent-streets-sn8l).
	steps[0].fn = func() error { return boom }
	got = runOptionalSteps(cmdutil.ErrNoResults, ios, steps)
	if !errors.Is(got, boom) || errors.Is(got, cmdutil.ErrNoResults) {
		t.Errorf("ErrNoResults + required failure = %v; want the failure alone", got)
	}
	// An empty result is still a quiet skip, required or not.
	steps[0].fn = func() error { return cmdutil.ErrNoResults }
	if got := runOptionalSteps(nil, ios, steps); got != nil {
		t.Errorf("ErrNoResults from a required step = %v, want a skip", got)
	}
}
