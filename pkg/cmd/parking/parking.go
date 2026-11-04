package parking

import (
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmd/resourcecmd"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"

	"github.com/spf13/cobra"
)

func NewCmdParking(f *cmdutil.Factory) *cobra.Command {
	return resourcecmd.New(f, &resource.Parking{}, resourcecmd.Spec{
		Use:   "parking",
		Short: "Manage parking lot data",
		Long:  "Ingest, compute, and view status for parking lot features.",
		Example: `  # Pull OSM parking polygons
  pvmt parking ingest --source overpass

  # Compute coverage from buffered parking polygons
  pvmt parking compute

  # Show last-ingested / last-computed timestamps
  pvmt parking status`,
	})
}
