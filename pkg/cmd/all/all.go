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
					return execSub(cmd.Context(), ingest.NewCmdIngest(cf, rt, nil), "--source", "all")
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
				if err := forEachResource(f.IOStreams, func(rt resource.Source) error {
					return execSub(cmd.Context(), compute.NewCmdCompute(cf, rt, nil))
				}); err != nil {
					return err
				}
				if err := compute.RunCombined(cmd.Context(), cf); err != nil {
					// A cancelled run must stop the whole fan-out rather than
					// proceed to the next city with partial data.
					if errors.Is(err, context.Canceled) {
						return err
					}
					cmdutil.Warnf(f.IOStreams, "combined pass failed: %v", err)
				}
				return nil
			})
		},
	}
}

func forEachResource(ios *iostreams.IOStreams, fn func(resource.Source) error) error {
	for _, rt := range resource.All {
		fmt.Fprintf(ios.ErrOut, "\n--- %s ---\n", rt.Type())
		if err := fn(rt); err != nil {
			if errors.Is(err, cmdutil.ErrNoResults) {
				continue
			}
			// A cancelled compute (SIGINT / TUI ctrl+c) must abort the
			// fan-out immediately: warn-and-continue would launch the next
			// resource's TUI while the interrupted one just refused to save,
			// and would mask the cancellation as exit 0.
			if errors.Is(err, context.Canceled) {
				return err
			}
			if errors.Is(err, cmdutil.ErrAllSourcesFailed) {
				return err
			}
			cmdutil.Warnf(ios, "%s failed: %v", rt.Type(), err)
		}
	}
	return nil
}

func execSub(ctx context.Context, cmd *cobra.Command, args ...string) error {
	// cobra.Command.SetArgs(nil) falls back to os.Args[1:]; force empty.
	if args == nil {
		args = []string{}
	}
	cmd.SetArgs(args)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	// ExecuteContext (not Execute) so the freshly-built child sees the parent's
	// signal-cancelled context; Execute would fall back to context.Background()
	// and leave the nested ingest/compute run uninterruptible.
	return cmd.ExecuteContext(ctx)
}
