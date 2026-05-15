package all

import (
	"errors"
	"fmt"

	"pvmt/internal/config"
	"pvmt/internal/resource"
	"pvmt/pkg/cmd/compute"
	"pvmt/pkg/cmd/ingest"
	"pvmt/pkg/cmdutil"
	"pvmt/pkg/iostreams"

	"github.com/spf13/cobra"
)

func NewCmdAll(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "all",
		Short: "Operate on all resource types",
	}

	cmd.AddCommand(newAllIngest(f))
	cmd.AddCommand(newAllCompute(f))

	return cmd
}

func newAllIngest(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "ingest",
		Short: "Ingest data for all resource types",
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
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdutil.ForEachCity(cmd.Context(), f, func(cf *cmdutil.Factory, _ *config.CityConfig) error {
				if err := forEachResource(f.IOStreams, func(rt resource.ResourceType) error {
					return execSub(compute.NewCmdCompute(cf, rt, nil))
				}); err != nil {
					return err
				}
				if err := compute.RunCombined(cmd.Context(), cf); err != nil {
					fmt.Fprintf(f.IOStreams.ErrOut, "Warning: combined pass failed: %v\n", err)
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
			fmt.Fprintf(ios.ErrOut, "Warning: %s failed: %v\n", rt.Name(), err)
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
