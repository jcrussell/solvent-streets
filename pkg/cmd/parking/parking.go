package parking

import (
	"pvmt/internal/resource"
	"pvmt/pkg/cmd/compute"
	"pvmt/pkg/cmd/ingest"
	"pvmt/pkg/cmd/status"
	"pvmt/pkg/cmdutil"

	"github.com/spf13/cobra"
)

func NewCmdParking(f *cmdutil.Factory) *cobra.Command {
	rt := &resource.Parking{}

	cmd := &cobra.Command{
		Use:   "parking",
		Short: "Manage parking lot data",
		Long:  "Ingest, compute, and view status for parking lot features.",
	}

	cmd.AddCommand(ingest.NewCmdIngest(f, rt, nil))
	cmd.AddCommand(compute.NewCmdCompute(f, rt, nil))
	cmd.AddCommand(status.NewCmdStatus(f, rt, nil))

	return cmd
}
