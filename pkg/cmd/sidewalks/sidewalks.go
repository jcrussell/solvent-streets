package sidewalks

import (
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmd/compute"
	"github.com/jcrussell/solvent-streets/pkg/cmd/ingest"
	"github.com/jcrussell/solvent-streets/pkg/cmd/status"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"

	"github.com/spf13/cobra"
)

func NewCmdSidewalks(f *cmdutil.Factory) *cobra.Command {
	rt := &resource.Sidewalk{}

	cmd := &cobra.Command{
		Use:   "sidewalks",
		Short: "Manage sidewalk data",
		Long:  "Ingest, compute, and view status for sidewalk features.",
		Example: `  # Pull OSM sidewalk linestrings
  pvmt sidewalks ingest --source overpass

  # Compute coverage from buffered sidewalks
  pvmt sidewalks compute

  # Show last-ingested / last-computed timestamps
  pvmt sidewalks status`,
	}

	cmd.AddCommand(ingest.NewCmdIngest(f, rt, nil))
	cmd.AddCommand(compute.NewCmdCompute(f, rt, nil))
	cmd.AddCommand(status.NewCmdStatus(f, rt, nil))

	return cmd
}
