package sidewalks

import (
	"pvmt/internal/resource"
	"pvmt/pkg/cmd/compute"
	"pvmt/pkg/cmd/ingest"
	"pvmt/pkg/cmd/status"
	"pvmt/pkg/cmdutil"

	"github.com/spf13/cobra"
)

func NewCmdSidewalks(f *cmdutil.Factory) *cobra.Command {
	rt := &resource.Sidewalk{}

	cmd := &cobra.Command{
		Use:   "sidewalks",
		Short: "Manage sidewalk data",
		Long:  "Ingest, compute, and view status for sidewalk features.",
	}

	cmd.AddCommand(ingest.NewCmdIngest(f, rt, nil))
	cmd.AddCommand(compute.NewCmdCompute(f, rt, nil))
	cmd.AddCommand(status.NewCmdStatus(f, rt, nil))

	return cmd
}
