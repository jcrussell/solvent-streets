package sidewalks

import (
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmd/resourcecmd"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"

	"github.com/spf13/cobra"
)

func NewCmdSidewalks(f *cmdutil.Factory) *cobra.Command {
	return resourcecmd.New(f, &resource.Sidewalk{}, resourcecmd.Spec{
		Use:   "sidewalks",
		Short: "Manage sidewalk data",
		Long:  "Ingest, compute, and view status for sidewalk features.",
		Example: `  # Pull OSM sidewalk linestrings
  pvmt sidewalks ingest --source overpass

  # Compute coverage from buffered sidewalks
  pvmt sidewalks compute

  # Show last-ingested / last-computed timestamps
  pvmt sidewalks status`,
	})
}
