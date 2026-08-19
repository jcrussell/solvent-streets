package validate

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestEvaluate_BerkeleyMatchesItsPublishedFigure is the calibration gate itself,
// asserted directly rather than through the CLI.
//
// Berkeley is the anchor: it is the only city with a clean, independent
// {budget, hold-steady $/m2-yr, paving cadence} triplet (docs/validation.md §6).
// If the modelled break-even drifts away from its published $18.3M, either the
// cost regime or the treatment cycle moved — those two are multiplicatively
// confounded in the dollar figures, so this catches the product of them, which
// is the thing that can actually be validated.
func TestEvaluate_BerkeleyMatchesItsPublishedFigure(t *testing.T) {
	report, err := Evaluate(DefaultTolerance)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if report.Failed != 0 {
		t.Errorf("%d gated cities failed at the shipped tolerance", report.Failed)
	}
	if report.Passed == 0 {
		t.Fatal("no city was gated; the reference data lost its published figures")
	}

	berkeley := findCity(t, report, "berkeley-ca")
	if berkeley.Residual == nil {
		t.Fatal("Berkeley has no residual; it must be gated")
	}
	if math.Abs(*berkeley.Residual) > DefaultTolerance {
		t.Errorf("Berkeley residual %+.1f%% exceeds ±%.0f%%: modelled %.0f vs published %.0f",
			*berkeley.Residual*100, DefaultTolerance*100, berkeley.BreakEven, berkeley.HoldSteadyBudget)
	}

	// The $/m2-yr yardstick from §6, which is what pins the cost x cycle balance.
	// Asserted separately from the residual: the dollar residual would still pass
	// if the area and the cost both moved by compensating factors.
	if berkeley.BreakEvenPerSqM < 4.5 || berkeley.BreakEvenPerSqM > 6.5 {
		t.Errorf("Berkeley break-even = $%.2f/m2-yr, outside the [$4.5, $6.5] bracket around the cited $5.6",
			berkeley.BreakEvenPerSqM)
	}
}

// TestEvaluate_UngatedCitiesAreReportedNotHidden: a city without a published
// hold-steady figure must still appear, marked as context. Dropping those rows
// would make the report look more validated than it is — five of the six cities
// have no independent target.
func TestEvaluate_UngatedCitiesAreReportedNotHidden(t *testing.T) {
	report, err := Evaluate(DefaultTolerance)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if report.Context == 0 {
		t.Error("no context-only rows; the report is claiming more validation than the data supports")
	}
	for _, r := range report.Results {
		if r.Status == "context" {
			if r.Residual != nil {
				t.Errorf("%s is context-only but carries a residual", r.Slug)
			}
			if r.Note == "" {
				t.Errorf("%s is context-only with no explanation", r.Slug)
			}
		}
		if r.BreakEven <= 0 {
			t.Errorf("%s: break-even %g; every row must model something", r.Slug, r.BreakEven)
		}
	}
}

// TestEvaluate_TightToleranceCanFail proves the gate is capable of failing.
// A gate that passes at every tolerance is measuring nothing.
func TestEvaluate_TightToleranceCanFail(t *testing.T) {
	report, err := Evaluate(1e-9)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if report.Failed == 0 {
		t.Error("no city failed at a 1e-9 tolerance; the comparison is not actually being made")
	}
}

// TestReferenceData_EveryRowIsCited: the whole value of this command is that a
// reader can check where its targets came from. An uncited row is a number
// someone made up.
func TestReferenceData_EveryRowIsCited(t *testing.T) {
	var data referenceData
	if err := json.Unmarshal(referenceJSON, &data); err != nil {
		t.Fatalf("parse reference.json: %v", err)
	}
	if len(data.Cities) == 0 {
		t.Fatal("no reference cities")
	}
	seen := map[string]bool{}
	for _, c := range data.Cities {
		if len(c.Sources) == 0 {
			t.Errorf("%s has no sources", c.Slug)
		}
		if c.PCI <= 0 || c.PCI > 100 {
			t.Errorf("%s: pci %g out of range", c.Slug, c.PCI)
		}
		if c.PavedAreaSqM <= 0 {
			t.Errorf("%s: paved_area_sqm %g must be positive", c.Slug, c.PavedAreaSqM)
		}
		if c.DecayRate <= 0 {
			t.Errorf("%s: decay_rate %g must be positive", c.Slug, c.DecayRate)
		}
		if seen[c.Slug] {
			t.Errorf("duplicate slug %s", c.Slug)
		}
		seen[c.Slug] = true
	}
}

func TestRun_FailingGateExitsSilentlyNonZero(t *testing.T) {
	ios, _, out, errOut := iostreams.Test()
	err := Run(context.Background(), &Options{IO: ios, Tolerance: 1e-9})

	if !errors.Is(err, cmdutil.ErrSilent) {
		t.Fatalf("Run = %v, want cmdutil.ErrSilent so the runner exits non-zero without re-printing", err)
	}
	// The table names the failures, so the error itself must not repeat them.
	if !strings.Contains(out.String(), "FAIL") && !strings.Contains(out.String(), "fail") {
		t.Errorf("the table did not mark any row failed:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "outside") {
		t.Errorf("no summary line on stderr:\n%s", errOut.String())
	}
}

// Data to Out, chatter to ErrOut (byob-iostreams.3), so `pvmt validate --json`
// pipes cleanly into jq.
func TestRun_JSONIsCleanOnStdout(t *testing.T) {
	ios, _, out, _ := iostreams.Test()
	if err := Run(context.Background(), &Options{IO: ios, Tolerance: DefaultTolerance, JSON: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var report Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("stdout is not valid JSON (%v):\n%s", err, out.String())
	}
	if len(report.Results) == 0 {
		t.Error("empty results in the JSON report")
	}
}

func TestOptions_ValidateRejectsBadTolerance(t *testing.T) {
	for _, tol := range []float64{0, -1, 11} {
		opts := &Options{Tolerance: tol}
		var flagErr *cmdutil.FlagError
		if err := opts.Validate(); !errors.As(err, &flagErr) {
			t.Errorf("tolerance %g: err = %v, want a FlagError", tol, err)
		}
	}
	if err := (&Options{Tolerance: DefaultTolerance}).Validate(); err != nil {
		t.Errorf("default tolerance rejected: %v", err)
	}
}

func TestNewCmdValidate_RunFOverride(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	var got *Options
	cmd := NewCmdValidate(&cmdutil.Factory{IOStreams: ios}, func(_ context.Context, o *Options) error {
		got = o
		return nil
	})
	cmd.SetArgs([]string{"--tolerance", "0.5", "--json"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got == nil {
		t.Fatal("runF was not called")
	}
	if got.Tolerance != 0.5 || !got.JSON {
		t.Errorf("flags not parsed into Options: %+v", got)
	}
}

func findCity(t *testing.T, r Report, slug string) Result {
	t.Helper()
	for _, res := range r.Results {
		if res.Slug == slug {
			return res
		}
	}
	t.Fatalf("city %q not in the report", slug)
	return Result{}
}
