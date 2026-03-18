package all

import (
	"errors"
	"fmt"

	"pvmt/internal/resource"
	"pvmt/pkg/cmd/compute"
	"pvmt/pkg/cmd/ingest"
	"pvmt/pkg/cmdutil"

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
	cmd := &cobra.Command{
		Use:   "ingest",
		Short: "Ingest data for all resource types",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAllIngest(f)
		},
	}
	return cmd
}

func runAllIngest(f *cmdutil.Factory) error {
	for _, rt := range resource.All {
		fmt.Fprintf(f.IOStreams.Out, "\n--- %s ---\n", rt.Name())
		sub := ingest.NewCmdIngest(f, rt, nil)
		sub.SetArgs([]string{"--source", "all"})
		sub.SilenceErrors = true
		sub.SilenceUsage = true
		if err := sub.Execute(); err != nil {
			if errors.Is(err, cmdutil.ErrNoResults) {
				continue
			}
			fmt.Fprintf(f.IOStreams.ErrOut, "Warning: %s ingest failed: %v\n", rt.Name(), err)
		}
	}
	return nil
}

func newAllCompute(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compute",
		Short: "Compute stats for all resource types",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAllCompute(f)
		},
	}
	return cmd
}

func runAllCompute(f *cmdutil.Factory) error {
	for _, rt := range resource.All {
		fmt.Fprintf(f.IOStreams.Out, "\n--- %s ---\n", rt.Name())
		sub := compute.NewCmdCompute(f, rt, nil)
		sub.SetArgs([]string{})
		sub.SilenceErrors = true
		sub.SilenceUsage = true
		if err := sub.Execute(); err != nil {
			if errors.Is(err, cmdutil.ErrNoResults) {
				continue
			}
			fmt.Fprintf(f.IOStreams.ErrOut, "Warning: %s compute failed: %v\n", rt.Name(), err)
		}
	}
	return nil
}
