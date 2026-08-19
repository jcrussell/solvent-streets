// Package validate implements `pvmt validate`: an offline calibration gate that
// checks the forecast model's solvency dollars against committed, cited figures
// from real cities.
//
// This promotes internal/forecast/backtest_test.go into a product surface. The
// test asserts two properties in Go; this reports the residuals as a table a
// reader can audit, and fails the same way in CI. It needs no database, no
// ingest and no network, so it can run anywhere the binary does.
package validate

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"text/tabwriter"

	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

//go:embed reference.json
var referenceJSON []byte

// referenceCity is one committed row: a real city with published condition and
// budget figures. See reference.json's _comment for what is cited and what is
// derived — notably PavedAreaSqM is derived from the tool's own output and is
// therefore NOT independent evidence about geometry.
type referenceCity struct {
	Slug             string   `json:"slug"`
	Name             string   `json:"name"`
	PCI              float64  `json:"pci"`
	DecayRate        float64  `json:"decay_rate"`
	PavedAreaSqM     float64  `json:"paved_area_sqm"`
	CurrentBudget    float64  `json:"current_budget"`
	HoldSteadyBudget float64  `json:"hold_steady_budget,omitempty"`
	Sources          []string `json:"sources"`
}

type yardstick struct {
	Value  float64 `json:"value"`
	Low    float64 `json:"low"`
	High   float64 `json:"high"`
	Source string  `json:"source"`
}

type referenceData struct {
	Yardstick yardstick       `json:"yardstick_per_sqm_year"`
	Cities    []referenceCity `json:"cities"`
}

// Result.Status values. A city is gated only when it publishes an independent
// hold-steady figure; everything else is statusContext, which the report shows
// rather than hides — see TestEvaluate_UngatedCitiesAreReportedNotHidden.
const (
	statusPass    = "pass"
	statusFail    = "fail"
	statusContext = "context"
)

// Result is one city's modelled-vs-published comparison. Emitted verbatim by
// --json so a downstream consumer gets the same numbers the table shows.
type Result struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	// BreakEven is the modelled smallest constant annual budget that holds the
	// network steady, with the treatment cycle applied.
	BreakEven float64 `json:"break_even"`
	// BreakEvenPerSqM is BreakEven / area — the figure §6's anchor is expressed
	// in, and the only one comparable across cities of different sizes.
	BreakEvenPerSqM float64 `json:"break_even_per_sqm_year"`
	CurrentBudget   float64 `json:"current_budget"`
	// HoldSteadyBudget is the city's own published hold-steady figure, when it
	// has one in citable form. Zero means "not published"; those rows are
	// reported for context and are not gated.
	HoldSteadyBudget float64 `json:"hold_steady_budget,omitempty"`
	// Residual is (modelled - published) / published against HoldSteadyBudget.
	// Nil when the city publishes no hold-steady figure.
	Residual *float64 `json:"residual,omitempty"`
	// Status is one of "pass", "fail", "context". "context" means there was
	// nothing independent to check against.
	Status string `json:"status"`
	// Note explains a failure, or why a row is context-only.
	Note string `json:"note,omitempty"`
}

// Report is the full --json payload.
type Report struct {
	Tolerance float64  `json:"tolerance"`
	Results   []Result `json:"results"`
	Passed    int      `json:"passed"`
	Failed    int      `json:"failed"`
	Context   int      `json:"context"`
}

// DefaultTolerance is the residual band a gated city must land in.
//
// ±25% is deliberately loose. The model is calibrated on ONE city (§6's Limit),
// its decay rates are not independently identifiable from public data (§2), and
// the condition spread is assumed rather than measured (§4). A tight band would
// encode false confidence and would fail on noise; this catches the thing the
// gate is actually for — a cost-regime or cycle change that moves the dollars by
// a factor, which is what a regression here would look like.
const DefaultTolerance = 0.25

type Options struct {
	IO        *iostreams.IOStreams
	Tolerance float64
	JSON      bool
}

func NewCmdValidate(f *cmdutil.Factory, runF func(context.Context, *Options) error) *cobra.Command {
	opts := &Options{IO: f.IOStreams, Tolerance: DefaultTolerance}

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Check forecast solvency dollars against published city figures",
		Long: `Compare the forecast model's break-even budget to committed, cited figures
from real cities.

This is an offline calibration gate: no database, no ingest, no network. The
reference data ships with the binary and every row carries its source. Exits
non-zero when a city with a published hold-steady budget falls outside the
tolerance band, so it can run in CI.

Only cities that publish an independent hold-steady figure are gated. Others
are reported for context — see docs/validation.md §6 for why the anchor rests
on Berkeley alone.`,
		Example: `  # Report residuals against published figures
  pvmt validate

  # Machine-readable, for a CI step or a dashboard
  pvmt validate --json

  # Tighten the band
  pvmt validate --tolerance 0.1`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.Validate(); err != nil {
				return err
			}
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return Run(cmd.Context(), opts)
		},
	}

	cmd.Flags().Float64Var(&opts.Tolerance, "tolerance", DefaultTolerance,
		"Maximum absolute residual before a city fails (0.25 = ±25%)")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "Emit the report as JSON instead of a table")

	return cmd
}

// Validate checks the flag values at the Options boundary, before any work.
func (o *Options) Validate() error {
	if !(o.Tolerance > 0 && o.Tolerance <= 10) {
		return cmdutil.FlagErrorf("--tolerance %g out of range (0-10, where 0.25 = ±25%%)", o.Tolerance)
	}
	return nil
}

func Run(_ context.Context, opts *Options) error {
	report, err := Evaluate(opts.Tolerance)
	if err != nil {
		return err
	}

	if opts.JSON {
		enc := json.NewEncoder(opts.IO.Out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return fmt.Errorf("encode report: %w", err)
		}
	} else {
		writeTable(opts.IO, report)
	}

	if report.Failed > 0 {
		// The table above already named every failing city and its residual, so
		// a top-level "Error: 1 of 1 outside ±25%" would just repeat it. Exit
		// non-zero quietly, per cmdutil.ErrSilent's contract.
		fmt.Fprintf(opts.IO.ErrOut, "%d of %d gated cities outside ±%.0f%%\n",
			report.Failed, report.Failed+report.Passed, opts.Tolerance*100)
		return cmdutil.ErrSilent
	}
	return nil
}

// Evaluate runs the model against every reference city and returns the report.
// Exported so a test — or a future dashboard — can assert on the numbers
// without going through the CLI.
func Evaluate(tolerance float64) (Report, error) {
	var data referenceData
	if err := json.Unmarshal(referenceJSON, &data); err != nil {
		return Report{}, fmt.Errorf("parse embedded reference data: %w", err)
	}

	report := Report{Tolerance: tolerance}
	for _, c := range data.Cities {
		report.Results = append(report.Results, evaluateCity(c, tolerance))
	}
	sort.Slice(report.Results, func(i, j int) bool {
		return report.Results[i].Slug < report.Results[j].Slug
	})
	for _, r := range report.Results {
		switch r.Status {
		case statusPass:
			report.Passed++
		case statusFail:
			report.Failed++
		default:
			report.Context++
		}
	}
	return report, nil
}

func evaluateCity(c referenceCity, tolerance float64) Result {
	// Spread the mean PCI exactly as the shipped export/CLI/WASM paths do, so
	// this measures the real pipeline rather than a flat-average shortcut. The
	// spread is load-bearing for the Berkeley match (§4 Resolution): without it
	// the figure lands ~19% low.
	cohorts := forecast.ApplyConditionSpread([]forecast.Cohort{{
		Classification: "residential",
		Area:           c.PavedAreaSqM,
		DecayRate:      c.DecayRate,
		InitialPCI:     c.PCI,
	}})

	// Growth 0.005 and a 25-year horizon match backtest_test.go and
	// examples/bay-area-ca. Cost overhead is left at bare (1) because the
	// shipped calibration is bare tiers x spread x N=12 — see
	// config.DefaultCostOverhead.
	params := forecast.NewParams(0.005, nil, forecast.DefaultTreatmentCycleYears, 1)
	breakEven := forecast.BreakEvenBudget(cohorts, 25, params, forecast.StrategyWorstFirst)

	res := Result{
		Slug:             c.Slug,
		Name:             c.Name,
		BreakEven:        breakEven,
		CurrentBudget:    c.CurrentBudget,
		HoldSteadyBudget: c.HoldSteadyBudget,
	}
	if c.PavedAreaSqM > 0 {
		res.BreakEvenPerSqM = breakEven / c.PavedAreaSqM
	}

	if c.HoldSteadyBudget <= 0 {
		res.Status = statusContext
		res.Note = "no published hold-steady budget; not gated"
		return res
	}

	residual := (breakEven - c.HoldSteadyBudget) / c.HoldSteadyBudget
	res.Residual = &residual
	if math.Abs(residual) <= tolerance {
		res.Status = statusPass
	} else {
		res.Status = statusFail
		res.Note = fmt.Sprintf("modelled %s vs published %s",
			fmtDollars(breakEven), fmtDollars(c.HoldSteadyBudget))
	}
	return res
}

func writeTable(io *iostreams.IOStreams, r Report) {
	cs := io.ColorScheme()
	w := tabwriter.NewWriter(io.Out, 0, 4, 2, ' ', 0)

	fmt.Fprintln(w, "CITY\tBREAK-EVEN\t$/SQ M·YR\tPUBLISHED\tRESIDUAL\tSTATUS")
	for _, res := range r.Results {
		published, residual := "—", "—"
		if res.HoldSteadyBudget > 0 {
			published = fmtDollars(res.HoldSteadyBudget)
		}
		if res.Residual != nil {
			residual = fmt.Sprintf("%+.1f%%", *res.Residual*100)
		}
		status := res.Status
		switch res.Status {
		case statusPass:
			status = cs.Green(statusPass)
		case statusFail:
			status = cs.Red("FAIL")
		}
		fmt.Fprintf(w, "%s\t%s\t$%.2f\t%s\t%s\t%s\n",
			res.Name, fmtDollars(res.BreakEven), res.BreakEvenPerSqM, published, residual, status)
	}
	_ = w.Flush()

	// Chatter to ErrOut so `pvmt validate > table.txt` yields only the table.
	fmt.Fprintf(io.ErrOut, "\n%d gated (%d pass, %d fail), %d context-only, tolerance ±%.0f%%\n",
		r.Passed+r.Failed, r.Passed, r.Failed, r.Context, r.Tolerance*100)
	fmt.Fprintln(io.ErrOut,
		"Only cities publishing an independent hold-steady budget are gated; see docs/validation.md §6.")
}

func fmtDollars(v float64) string {
	if v >= 1e6 {
		return fmt.Sprintf("$%.1fM", v/1e6)
	}
	return fmt.Sprintf("$%.0f", v)
}
