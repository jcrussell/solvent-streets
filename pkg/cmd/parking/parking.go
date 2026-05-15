package parking

import (
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmd/compute"
	"github.com/jcrussell/solvent-streets/pkg/cmd/ingest"
	"github.com/jcrussell/solvent-streets/pkg/cmd/status"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"

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
