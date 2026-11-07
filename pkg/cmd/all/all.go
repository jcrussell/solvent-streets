package all

import (
	"context"
	"errors"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/resource"
	censuscmd "github.com/jcrussell/solvent-streets/pkg/cmd/census"
	"github.com/jcrussell/solvent-streets/pkg/cmd/compute"
	"github.com/jcrussell/solvent-streets/pkg/cmd/ingest"
	servicescmd "github.com/jcrussell/solvent-streets/pkg/cmd/services"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

func NewCmdAll(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "all",
		Short: "Operate on all resource types",
		Long: `Fan out the ingest or compute pipeline across every pavement resource
(roads, parking, sidewalks) for every [[cities]] entry in pvmt.toml, then
run the fire-station and Census steps ('pvmt services', 'pvmt census')
best-effort: a failure there is warned about and does not fail the run.
A city with census = false skips the Census steps.

'all compute' also runs the combined paved-area pass, which unions the
per-resource geometries to avoid double-counting overlap, before fire
response coverage and per-hex population.`,
		Example: `  # Ingest roads, parking, sidewalks, fire stations and Census blocks
  pvmt all ingest

  # Compute pavement coverage, the combined paved area, fire coverage and population
  pvmt all compute`,
	}

	cmdutil.MarkGroup(cmd)
	cmd.AddCommand(newAllIngest(f))
	cmd.AddCommand(newAllCompute(f))

	return cmd
}

func newAllIngest(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "ingest",
		Short: "Ingest data for all resource types",
		Example: `  # Pull roads, parking, sidewalks, fire stations and Census blocks for every [[cities]] entry
  pvmt all ingest`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdutil.ForEachCity(cmd.Context(), f, func(cf *cmdutil.Factory, city *config.CityConfig) error {
				return ingestCity(cmd.Context(), f.IOStreams, cf, city)
			})
		},
	}
}

// ingestCity runs every ingest step for one city. The boundary (and its
// water strip, the heaviest Overpass query) is resolved once up front: every
// step needs it, so when it cannot be had the city is skipped with one
// warning instead of each step re-fetching it and failing the same way.
func ingestCity(ctx context.Context, ios *iostreams.IOStreams, cf *cmdutil.Factory, city *config.CityConfig) error {
	boundary, err := ingest.ResolveBoundaryForCity(ctx, cf)
	if err != nil {
		if !isCancel(err) {
			cmdutil.Warnf(ios, "skipping %s: no boundary: %v", city.Name, err)
		}
		return err
	}
	err = forEachResource(ios, func(rt resource.Source) error {
		return ingest.RunResourceWithBoundary(ctx, cf, rt, boundary)
	})
	if isCancel(err) {
		return err
	}
	// Facility and Census ingest ride along best-effort (see
	// runOptionalStep).
	return runOptionalSteps(err, ios, []optionalStep{
		{servicescmd.DefaultService + " ingest", func() error {
			return servicescmd.RunIngestForCity(ctx, cf, servicescmd.DefaultService)
		}},
		{"census ingest", func() error { return censuscmd.RunIngestForCity(ctx, cf) }},
	})
}

func newAllCompute(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "compute",
		Short: "Compute stats for all resource types",
		Example: `  # Compute every resource and then the combined paved-area pass
  pvmt all compute`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdutil.ForEachCity(cmd.Context(), f, func(cf *cmdutil.Factory, city *config.CityConfig) error {
				return computeCity(cmd.Context(), f.IOStreams, cf, city)
			})
		},
	}
}

// computeCity runs every compute step for one city into ONE draft and commits
// it as the city's next snapshot: the steps that succeeded swap in their
// parts, a failed step leaves its part as it was, and a cancellation commits
// nothing (solvent-streets-25t6).
func computeCity(ctx context.Context, ios *iostreams.IOStreams, cf *cmdutil.Factory, city *config.CityConfig) error {
	cfg, err := cf.Config()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	store, err := cf.CityDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	draft := db.NewDraft(store, cfg.CityHash(city))
	stepsErr := computeSteps(ctx, ios, cf, draft)
	if isCancel(stepsErr) {
		return errors.Join(stepsErr, draft.Discard(context.WithoutCancel(ctx)))
	}
	if _, err := draft.Commit(ctx); err != nil {
		return compute.CommitFailed(stepsErr, err, draft.Discard(context.WithoutCancel(ctx)))
	}
	return stepsErr
}

// computeSteps runs the per-resource passes, the combined pass and the
// best-effort metric producers for one city, all into draft.
func computeSteps(ctx context.Context, ios *iostreams.IOStreams, cf *cmdutil.Factory, draft *db.Draft) error {
	// Build the clipped hex grid once per city and share it across the
	// three per-resource passes and the combined pass — the grid
	// depends only on (boundary, hex edge) and is identical for all
	// four, so this replaces the old 4× rebuild/clip. If it can't be
	// built (missing boundary, DB error, ...) every pass would fail
	// the same way, so surface it once and skip the city rather than
	// re-running the identical failure three times.
	grid, err := compute.BuildCityGrid(ctx, cf)
	if err != nil {
		return err
	}
	resErr := forEachResource(ios, func(rt resource.Source) error {
		return compute.RunResourceForCity(ctx, cf, rt, grid, draft)
	})
	// Cancellation stops the city here: the combined pass would fail
	// the same way, and running it on a half-computed city would
	// overwrite a good combined row with a partial one.
	if errors.Is(resErr, context.Canceled) || errors.Is(resErr, context.DeadlineExceeded) {
		return resErr
	}
	// Any OTHER per-resource failure is reported but must not skip
	// the combined pass. forEachResource returns those errors now
	// (so the run exits non-zero) rather than the nil it used to,
	// and an early return here would silently drop the combined
	// paved-area row for every city where one resource failed —
	// leaving the export with no cross-resource total.
	// A failed combined pass is fatal to the RUN, exactly like a
	// failed per-resource pass. It used to warn and return resErr —
	// which is nil whenever all three resources succeeded, so any
	// other failure (a locked database, a boundary read error, a
	// store write error) exited 0. ForEachCity propagates that nil
	// either way: it short-circuits single-city configs with a bare
	// return, and a nil return marks the multi-city loop as having
	// seen a result. `pvmt all compute && pvmt export` then
	// published a site whose cross-resource paved-area total was
	// the previous run's, or absent — the "sail on and publish"
	// failure 336c1cd set out to prevent.
	//
	// Joining also subsumes the cancellation case the old code
	// special-cased, and picks up DeadlineExceeded, which that
	// check omitted (unlike its siblings above and in
	// forEachResource). No Warnf here: the returned error is
	// printed by the top-level runner, so warning as well would
	// print it twice.
	if err := compute.RunCombined(ctx, cf, grid, draft); err != nil {
		// Do NOT join when resErr is ErrNoResults. That sentinel is
		// matched ahead of the generic arm by both ForEachCity
		// (which files it under skippedEmpty, not errs, so a
		// multi-city run still exits 0) and exitCode (which maps it
		// to a silent 3). Joining would let "every resource was
		// empty" swallow a real combined-pass failure — the exact
		// masking this fix exists to remove. The combined error is
		// the more serious signal, so it wins outright.
		if errors.Is(resErr, cmdutil.ErrNoResults) {
			return err
		}
		return errors.Join(resErr, err)
	}
	// Service coverage and population ride along best-effort after
	// the pavement passes, on the same shared grid (see
	// runOptionalStep).
	return runOptionalSteps(resErr, ios, []optionalStep{
		{servicescmd.DefaultService + " compute", func() error {
			return servicescmd.RunComputeForCity(ctx, cf, servicescmd.DefaultService, grid, draft)
		}},
		{"census compute", func() error { return censuscmd.RunComputeForCity(ctx, cf, grid, draft) }},
	})
}

// isCancel reports a context cancellation or deadline, which must abort the
// fan-out rather than be warned about.
func isCancel(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// optionalStep is one per-hex metric producer step (fire coverage,
// population) that rides along after the pavement passes.
type optionalStep struct {
	label string
	fn    func() error
}

// runOptionalSteps runs each step best-effort (runOptionalStep) and folds the
// outcome into the pavement result: a cancellation stops the remaining steps
// and wins; otherwise the pavement result stands.
func runOptionalSteps(resErr error, ios *iostreams.IOStreams, steps []optionalStep) error {
	for _, st := range steps {
		if err := runOptionalStep(ios, st.label, st.fn); isCancel(err) {
			return err
		}
	}
	return resErr
}

// runOptionalStep runs one optional step best-effort: cancellation is
// returned as-is so the city aborts; ErrNoResults (no stations mapped, no
// Census blocks, a city that opted out — the common case in a large config)
// is a silent skip exactly like an empty pavement resource; any other failure
// is warned about and dropped so the pavement outcome, exit code and combined
// pass are unchanged. The step is optional output, so it never fails a run
// on its own.
func runOptionalStep(ios *iostreams.IOStreams, label string, fn func() error) error {
	fmt.Fprintf(ios.ErrOut, "\n--- %s ---\n", label)
	err := fn()
	switch {
	case err == nil, errors.Is(err, cmdutil.ErrNoResults):
		return nil
	case isCancel(err):
		return err
	default:
		cmdutil.Warnf(ios, "%s failed: %v", label, err)
		return nil
	}
}

func forEachResource(ios *iostreams.IOStreams, fn func(resource.Source) error) error {
	var errs []error
	sawResult := false    // at least one resource produced something
	skippedEmpty := false // at least one resource came back empty
	for _, rt := range resource.All {
		fmt.Fprintf(ios.ErrOut, "\n--- %s ---\n", rt.Type())
		err := fn(rt)
		if err == nil {
			sawResult = true
		}
		if err != nil {
			if errors.Is(err, cmdutil.ErrNoResults) {
				skippedEmpty = true
				continue
			}
			// A cancelled compute (SIGINT / TUI ctrl+c) must abort the
			// fan-out immediately: warn-and-continue would launch the next
			// resource's TUI while the interrupted one just refused to save,
			// and would mask the cancellation as exit 0.
			//
			// DeadlineExceeded gets the same treatment: an expired context does
			// not un-expire, so every remaining resource would fail identically
			// and the run would still exit 0. It also fell through the old
			// Canceled-only check, which would have swallowed the ctx.Err()
			// pre-checks that ingest/compute.RunResourceForCity now return.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			// A total-source-failure for ONE resource is non-fatal to the
			// REST of the fan-out: warn and move on so the remaining resources
			// (e.g. parking, sidewalks) still run instead of being skipped by an
			// early return (finding 0yfp). It is still fatal to the RUN, though
			// — dropping it here made `pvmt all ingest` exit 0 with nothing
			// ingested, so a chained `ingest && compute && export` would sail on
			// and publish an empty site. Collect and join instead.
			cmdutil.Warnf(ios, "%s failed: %v", rt.Type(), err)
			errs = append(errs, fmt.Errorf("%s: %w", rt.Type(), err))
		}
	}
	// Every resource came back empty and none produced anything: propagate
	// ErrNoResults so the city exits 3, the same as `pvmt roads ingest` does
	// for the identical condition. Returning nil here let `all ingest` exit 0
	// on a city that ingested nothing at all, and a chained
	// `ingest && compute && export` would publish the empty result.
	// ForEachCity applies the same sawResult/skippedEmpty rule one level up,
	// so a run is only ErrNoResults when EVERY city was empty.
	if len(errs) == 0 && !sawResult && skippedEmpty {
		return cmdutil.ErrNoResults
	}
	return errors.Join(errs...)
}
