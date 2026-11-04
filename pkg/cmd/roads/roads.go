package roads

import (
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmd/resourcecmd"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"

	"github.com/spf13/cobra"
)

func NewCmdRoads(f *cmdutil.Factory) *cobra.Command {
	return resourcecmd.New(f, &resource.Pavement{}, resourcecmd.Spec{
		Use:   "roads",
		Short: "Manage road/pavement data",
		Long:  "Ingest, compute, and view status for road and pavement features.",
		Example: `  # Pull OSM road geometries for the configured cities
  pvmt roads ingest --source overpass

  # Compute paved-area coverage per hex
  pvmt roads compute

  # Print current row counts and last-compute timestamp
  pvmt roads status`,
	})
}
