package all

import (
	"context"
	"errors"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmd/compute"
	"github.com/jcrussell/solvent-streets/pkg/cmd/ingest"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

func NewCmdAll(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "all",
		Short: "Operate on all resource types",
		Long: `Fan out the ingest or compute pipeline across every resource type
(roads, parking, sidewalks) for every [[cities]] entry in pvmt.toml.
'all compute' additionally runs the combined paved-area pass that
unions the per-resource geometries to avoid double-counting overlap.`,
		Example: `  # Ingest roads, parking, and sidewalks from all configured sources
  pvmt all ingest

  # Compute coverage for each resource and the combined paved area
  pvmt all compute`,
	}

	cmd.AddCommand(newAllIngest(f))
	cmd.AddCommand(newAllCompute(f))

	return cmd
}

func newAllIngest(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "ingest",
		Short: "Ingest data for all resource types",
		Example: `  # Pull roads, parking, sidewalks across every configured [[cities]]
  pvmt all ingest`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdutil.ForEachCity(cmd.Context(), f, func(cf *cmdutil.Factory, _ *config.CityConfig) error {
				return forEachResource(f.IOStreams, func(rt resource.Source) error {
					return ingest.RunResourceForCity(cmd.Context(), cf, rt)
				})
			})
		},
	}
}

func newAllCompute(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "compute",
		Short: "Compute stats for all resource types",
		Example: `  # Compute every resource and then the combined paved-area pass
  pvmt all compute`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdutil.ForEachCity(cmd.Context(), f, func(cf *cmdutil.Factory, _ *config.CityConfig) error {
				// Build the clipped hex grid once per city and share it across the
				// three per-resource passes and the combined pass — the grid
				// depends only on (boundary, hex edge) and is identical for all
				// four, so this replaces the old 4× rebuild/clip. If it can't be
				// built (missing boundary, DB error, ...) every pass would fail
				// the same way, so surface it once and skip the city rather than
				// re-running the identical failure three times.
				grid, err := compute.BuildCityGrid(cmd.Context(), cf)
				if err != nil {
					return err
				}
				resErr := forEachResource(f.IOStreams, func(rt resource.Source) error {
					return compute.RunResourceForCity(cmd.Context(), cf, rt, grid)
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
				if err := compute.RunCombined(cmd.Context(), cf, grid); err != nil {
					if errors.Is(err, context.Canceled) {
						return errors.Join(resErr, err)
					}
					cmdutil.Warnf(f.IOStreams, "combined pass failed: %v", err)
				}
				return resErr
			})
		},
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
