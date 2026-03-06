package roads

import (
	"pvmt/internal/resource"
	"pvmt/pkg/cmd/compute"
	"pvmt/pkg/cmd/ingest"
	"pvmt/pkg/cmd/status"
	"pvmt/pkg/cmdutil"

	"github.com/spf13/cobra"
)

func NewCmdRoads(f *cmdutil.Factory) *cobra.Command {
	rt := &resource.Pavement{}

	cmd := &cobra.Command{
		Use:   "roads",
		Short: "Manage road/pavement data",
		Long:  "Ingest, compute, and view status for road and pavement features.",
	}

	cmd.AddCommand(ingest.NewCmdIngest(f, rt, nil))
	cmd.AddCommand(compute.NewCmdCompute(f, rt, nil))
	cmd.AddCommand(status.NewCmdStatus(f, rt, nil))

	return cmd
}
