package all

import (
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
			for _, rt := range resource.All {
				fmt.Fprintf(f.IOStreams.Out, "\n--- %s ---\n", rt.Name())
				subCmd := ingest.NewCmdIngest(f, rt, nil)
				subCmd.SetArgs([]string{})
				if err := subCmd.Execute(); err != nil {
					fmt.Fprintf(f.IOStreams.ErrOut, "Warning: %s ingest failed: %v\n", rt.Name(), err)
				}
			}
			return nil
		},
	}
	return cmd
}

func newAllCompute(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compute",
		Short: "Compute stats for all resource types",
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, rt := range resource.All {
				fmt.Fprintf(f.IOStreams.Out, "\n--- %s ---\n", rt.Name())
				subCmd := compute.NewCmdCompute(f, rt, nil)
				subCmd.SetArgs([]string{})
				if err := subCmd.Execute(); err != nil {
					fmt.Fprintf(f.IOStreams.ErrOut, "Warning: %s compute failed: %v\n", rt.Name(), err)
				}
			}
			return nil
		},
	}
	return cmd
}
