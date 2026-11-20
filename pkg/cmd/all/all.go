package all

import (
	"context"
	"errors"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/cache"
	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/hint"
	"github.com/jcrussell/solvent-streets/internal/logs"
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
run the fire-station and Census steps ('pvmt services', 'pvmt census').
'all ingest' keeps going past a failed step, so every other step and city
still runs, then exits 1 and lists each failure with the command that
retries it. 'all compute' runs the fire and Census steps best-effort: a
failure there is warned about and does not fail the run. A city with
census = false skips the Census steps.

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
	var force, rebuild, dryRun bool
	cmd := &cobra.Command{
		Use:   "ingest",
		Args:  cobra.NoArgs,
		Short: "Ingest data for all resource types",
		Long: `Run every ingest step for every configured city, or the one --city names.
The city boundary is resolved once; a city whose boundary cannot be had is
skipped with a warning. A fresh boundary fetches the city's roads with it,
as land evidence for its water strip, and the roads step reports those.
Roads, parking and sidewalks are otherwise fetched from the city's sources,
followed by fire stations and Census blocks. A step
that fails, or a resource one of whose sources fails, is warned about and
the run goes on; it then exits 1, and the error lists each failed step
with the command that retries it.

Responses come from the HTTP cache while they are younger than the
[cache] ttl (a week by default). --force bypasses the cache for every step
and re-fetches the boundary, for data fresh from upstream.
--rebuild-boundary re-resolves a stored boundary through the cache without
bypassing it, so a change to the water strip can be replayed; any request
the cache does not hold (such as roads over an unstripped bbox no earlier
ingest queried) goes upstream.

--dry-run prints every step's plan, as the per-resource --dry-run does,
without fetching anything or storing features; each city's boundary plan
is printed once. Unlike the per-resource ingest
commands this takes no --source; use 'pvmt <resource> ingest' for that.
Run 'pvmt all compute' next.`,
		Example: `  # Pull roads, parking, sidewalks, fire stations and Census blocks for every [[cities]] entry
  pvmt all ingest

  # Re-fetch one city from upstream, bypassing the HTTP cache
  pvmt --city livermore-ca all ingest --force

  # Re-resolve one city's stored boundary through the HTTP cache
  pvmt --city livermore-ca all ingest --rebuild-boundary

  # Show what every step would fetch, without making any requests
  pvmt all ingest --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if force {
				ctx = cache.WithBypass(ctx)
			}
			if dryRun {
				ctx = ingest.WithDryRun(ctx)
			}
			return cmdutil.ForEachCity(ctx, f, func(ctx context.Context, cf *cmdutil.Factory, city *config.CityConfig) error {
				return ingestCity(ctx, f.IOStreams, cf, city, force || rebuild)
			})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Bypass the HTTP cache for every step and re-fetch the city boundary")
	cmd.Flags().BoolVar(&rebuild, "rebuild-boundary", false, "Re-resolve the stored city boundary and its water strip, through the HTTP cache")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Resolve sources and print every step's plan without fetching anything or storing features")
	cmd.MarkFlagsMutuallyExclusive("force", "dry-run")
	cmd.MarkFlagsMutuallyExclusive("rebuild-boundary", "dry-run")
	return cmd
}

// ingestCity runs every ingest step for one city. The boundary (and its
// water strip, the heaviest Overpass query) is resolved once up front: every
// step needs it, so when it cannot be had the city is skipped with one
// warning instead of each step re-fetching it and failing the same way.
//
// rebuild re-resolves a stored boundary (--rebuild-boundary, or --force,
// whose cache bypass is already on ctx).
func ingestCity(ctx context.Context, ios *iostreams.IOStreams, cf *cmdutil.Factory, city *config.CityConfig, rebuild bool) error {
	// A dry run resolves nothing up front: each step's plan says how it would
	// get the boundary.
	var boundary *ingest.Boundary
	var err error
	if !ingest.IsDryRun(ctx) {
		boundary, err = ingest.ResolveBoundaryForCity(ctx, cf, rebuild)
	}
	if err != nil {
		if !isCancel(err) {
			cmdutil.Warnf(ios, "skipping %s: no boundary: %v", city.Name, err)
		}
		return err
	}
	retry := func(err error, cmd string) error {
		if err == nil || isCancel(err) || errors.Is(err, cmdutil.ErrNoResults) {
			return err
		}
		return hint.Addf(err, "retry it with 'pvmt --city %s %s ingest'", city.Slug(), cmd)
	}
	err = forEachResource(ios, "ingest", func(rt resource.Source) error {
		return retry(ingest.RunResourceWithBoundary(ctx, cf, rt, boundary), string(rt.Type()))
	})
	if isCancel(err) {
		return err
	}
	// Facility and Census ingest run after the pavement resources. Unlike in
	// `all compute`, a failure here fails the run (after every step has had
	// its turn): a city published without its fire stations or population is
	// incomplete, and only the exit status reaches a chained
	// `ingest && compute && export`.
	return runOptionalSteps(err, ios, []optionalStep{
		{label: servicescmd.DefaultService + " ingest", required: true, fn: func() error {
			return retry(servicescmd.RunIngestForCity(ctx, cf, servicescmd.DefaultService), "services")
		}},
		{label: "census ingest", required: true, fn: func() error {
			return retry(censuscmd.RunIngestForCity(ctx, cf), "census")
		}},
	})
}

func newAllCompute(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "compute",
		Args:  cobra.NoArgs,
		Short: "Compute stats for all resource types",
		Long: `Run every compute step for every configured city, or the one --city names,
on one shared hex grid: roads, parking and sidewalks coverage, the combined
paved-area pass, then best-effort fire response coverage and per-hex
population. All of a city's steps update its latest compute together; run
'pvmt snapshots create <name>' to publish it. A failed pavement or combined step
keeps its previous part and fails the run; a failed optional step only
warns. An interrupted run commits nothing.

Needs a prior 'pvmt all ingest'. Rerun after re-ingesting or after changing
a setting that affects results.`,
		Example: `  # Compute every resource and then the combined paved-area pass
  pvmt all compute`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdutil.ForEachCity(cmd.Context(), f, func(ctx context.Context, cf *cmdutil.Factory, city *config.CityConfig) error {
				return computeCity(ctx, f.IOStreams, cf, city)
			})
		},
	}
}

// computeCity runs every compute step for one city into ONE draft and commits
// it into the city's latest compute: the steps that succeeded swap in their
// parts, a failed step leaves its part as it was, and a cancellation commits
// nothing.
func computeCity(ctx context.Context, ios *iostreams.IOStreams, cf *cmdutil.Factory, city *config.CityConfig) error {
	logs.From(ctx).Debug("all compute: city start")
	cfg, err := cf.Config()
	if err != nil {
		return err
	}
	store, err := cf.CityDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	draft := compute.NewDraft(store, cfg, city)
	stepsErr := computeSteps(ctx, ios, cf, draft)
	if isCancel(stepsErr) {
		return errors.Join(stepsErr, draft.Discard(context.WithoutCancel(ctx)))
	}
	if err := draft.Commit(ctx); err != nil {
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
	// four, so building it per pass would be wasted work. If it can't be
	// built (missing boundary, DB error, ...) every pass would fail
	// the same way, so surface it once and skip the city rather than
	// re-running the identical failure three times.
	//
	// Every step records the boundary digest read here, before the grid is
	// built from the boundary, not the one it reads itself later: a boundary
	// ingest that lands in between then marks the steps stale instead of
	// passing a grid from the old boundary off as current.
	if err := pinBoundary(ctx, cf, draft); err != nil {
		return err
	}
	grid, err := compute.BuildCityGrid(ctx, cf)
	if err != nil {
		return err
	}
	resErr := forEachResource(ios, "compute", func(rt resource.Source) error {
		return compute.RunResourceForCity(ctx, cf, rt, grid, draft)
	})
	// Cancellation stops the city here: the combined pass would fail
	// the same way, and running it on a half-computed city would
	// overwrite a good combined row with a partial one.
	if isCancel(resErr) {
		return resErr
	}
	// Any OTHER per-resource failure is reported but must not skip
	// the combined pass. forEachResource returns those errors (so
	// the run exits non-zero), and an early return here would
	// silently drop the combined paved-area row for every city
	// where one resource failed — leaving the export with no
	// cross-resource total.
	// A failed combined pass is fatal to the RUN, exactly like a
	// failed per-resource pass. Warning and returning resErr would
	// not do: resErr is nil whenever all three resources succeeded,
	// so any other failure (a locked database, a boundary read
	// error, a store write error) would exit 0. ForEachCity
	// propagates that nil either way: it short-circuits single-city
	// configs with a bare return, and a nil return marks the
	// multi-city loop as having seen a result. `pvmt all compute &&
	// pvmt export` would then publish a site whose cross-resource
	// paved-area total was the previous run's, or absent — the
	// "sail on and publish" failure 336c1cd set out to prevent.
	//
	// Joining also covers cancellation, including DeadlineExceeded,
	// with no special case. No Warnf here: the returned error is
	// printed by the top-level runner, so warning as well would
	// print it twice.
	if err := compute.RunCombined(ctx, cf, grid, draft); err != nil {
		// Do NOT join when resErr is ErrNoResults. That sentinel is
		// matched ahead of the generic arm by both ForEachCity
		// (which files it under skippedEmpty, not errs, so a
		// multi-city run still exits 0) and exitCode (which maps it
		// to a silent 3). Joining would let "every resource was
		// empty" swallow a real combined-pass failure — the exact
		// masking the join exists to prevent. The combined error is
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
		{label: servicescmd.DefaultService + " compute", fn: func() error {
			return servicescmd.RunComputeForCity(ctx, cf, servicescmd.DefaultService, grid, draft)
		}},
		{label: "census compute", fn: func() error { return censuscmd.RunComputeForCity(ctx, cf, grid, draft) }},
	})
}

// pinBoundary pins the city's current boundary digest on draft (see
// db.Draft.PinInput).
func pinBoundary(ctx context.Context, cf *cmdutil.Factory, draft *db.Draft) error {
	store, err := cf.CityDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	digests, err := store.InputDigests(ctx, db.InputBoundary)
	if err != nil {
		return err
	}
	draft.PinInput(db.InputBoundary, digests[db.InputBoundary])
	return nil
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
	// required makes a failure fail the run once every step has run, instead
	// of only warning (ingest; see ingestCity).
	required bool
}

// runOptionalSteps runs each step best-effort (runOptionalStep) and folds the
// outcome into the pavement result: a cancellation stops the remaining steps
// and wins; otherwise the pavement result stands.
//
// A required step's failure is warned about like any other and the remaining
// steps still run, but it is joined into the result. When the pavement result
// is ErrNoResults, a required failure replaces it rather than joining it: the
// callers match ErrNoResults first (Outcomes.Add, ForEachCity, exitCode) and
// would file the city as empty, swallowing the failure.
func runOptionalSteps(resErr error, ios *iostreams.IOStreams, steps []optionalStep) error {
	var failures []error
	for _, st := range steps {
		err := runOptionalStep(ios, st.label, st.fn)
		switch {
		case isCancel(err):
			return err
		case err != nil && st.required:
			failures = append(failures, fmt.Errorf("%s: %w", st.label, err))
		case err != nil:
			// A best-effort failure is dropped after its warning, so its
			// remediation hint is shown here or never.
			// A required one keeps it for the final error.
			hint.Fprint(ios.ErrOut, err)
		}
	}
	if len(failures) == 0 {
		return resErr
	}
	if errors.Is(resErr, cmdutil.ErrNoResults) {
		return errors.Join(failures...)
	}
	return errors.Join(append([]error{resErr}, failures...)...)
}

// runOptionalStep runs one optional step best-effort: cancellation is
// returned as-is so the city aborts; ErrNoResults (no stations mapped, no
// Census blocks, a city that opted out — the common case in a large config)
// is a silent skip exactly like an empty pavement resource; any other failure
// is warned about and returned. runOptionalSteps drops it for a best-effort
// step (compute: the pavement outcome, exit code and
// combined pass are unchanged) and keeps it for a required one (ingest).
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
		return err
	}
}

// forEachResource runs fn for each pavement resource under a
// "--- <resource> <verb> ---" header (verb is "ingest" or "compute").
func forEachResource(ios *iostreams.IOStreams, verb string, fn func(resource.Source) error) error {
	// cmdutil.Outcomes is the rule ForEachCity applies one level up: an empty
	// resource is skipped, a failed one is kept while the rest still run, and
	// the city is ErrNoResults (exit 3, as `pvmt roads ingest` is for the same
	// condition) only when every resource was empty. A failure must stay fatal
	// to the run: dropping it would let `pvmt all ingest` exit 0 with nothing
	// ingested, and a chained `ingest && compute && export` would publish an
	// empty site.
	var out cmdutil.Outcomes
	for _, rt := range resource.All {
		fmt.Fprintf(ios.ErrOut, "\n--- %s %s ---\n", rt.Type(), verb)
		err := fn(rt)
		// A canceled compute (SIGINT / TUI ctrl+c) must abort the fan-out
		// immediately: warn-and-continue would launch the next resource's TUI
		// while the interrupted one just refused to save, and would mask the
		// cancellation as exit 0.
		//
		// DeadlineExceeded gets the same treatment: an expired context does
		// not un-expire, so every remaining resource would fail identically.
		// Checking only Canceled would also swallow the ctx.Err() pre-checks
		// that ingest/compute.RunResourceForCity return.
		if isCancel(err) {
			return err
		}
		if err != nil && !errors.Is(err, cmdutil.ErrNoResults) {
			cmdutil.Warnf(ios, "%s failed: %v", rt.Type(), err)
		}
		// Ingest errors already lead with the resource ("roads: all sources
		// failed"); Add does not prefix those again.
		out.Add(string(rt.Type()), err)
	}
	return out.Err()
}
