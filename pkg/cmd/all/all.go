package all

import (
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
				return forEachResource(f.IOStreams, func(rt resource.ResourceType) error {
					return execSub(ingest.NewCmdIngest(cf, rt, nil), "--source", "all")
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
				if err := forEachResource(f.IOStreams, func(rt resource.ResourceType) error {
					return execSub(compute.NewCmdCompute(cf, rt, nil))
				}); err != nil {
					return err
				}
				if err := compute.RunCombined(cmd.Context(), cf); err != nil {
					cmdutil.Warnf(f.IOStreams, "combined pass failed: %v", err)
				}
				return nil
			})
		},
	}
}

func forEachResource(ios *iostreams.IOStreams, fn func(resource.ResourceType) error) error {
	for _, rt := range resource.All {
		fmt.Fprintf(ios.ErrOut, "\n--- %s ---\n", rt.Name())
		if err := fn(rt); err != nil {
			if errors.Is(err, cmdutil.ErrNoResults) {
				continue
			}
			if errors.Is(err, cmdutil.ErrAllSourcesFailed) {
				return err
			}
			cmdutil.Warnf(ios, "%s failed: %v", rt.Name(), err)
		}
	}
	return nil
}

func execSub(cmd *cobra.Command, args ...string) error {
	// cobra.Command.SetArgs(nil) falls back to os.Args[1:]; force empty.
	if args == nil {
		args = []string{}
	}
	cmd.SetArgs(args)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	return cmd.Execute()
}
