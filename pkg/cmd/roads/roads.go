package roads

import (
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmd/compute"
	"github.com/jcrussell/solvent-streets/pkg/cmd/ingest"
	"github.com/jcrussell/solvent-streets/pkg/cmd/status"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"

	"github.com/spf13/cobra"
)

func NewCmdRoads(f *cmdutil.Factory) *cobra.Command {
	rt := &resource.Pavement{}

	cmd := &cobra.Command{
		Use:   "roads",
		Short: "Manage road/pavement data",
		Long:  "Ingest, compute, and view status for road and pavement features.",
		Example: `  # Pull OSM road geometries for the configured cities
  pvmt roads ingest --source overpass

  # Compute paved-area coverage per hex
  pvmt roads compute

  # Print current row counts and last-compute timestamp
  pvmt roads status`,
	}

	cmd.AddCommand(ingest.NewCmdIngest(f, rt, nil))
	cmd.AddCommand(compute.NewCmdCompute(f, rt, nil))
	cmd.AddCommand(status.NewCmdStatus(f, rt, nil))

	return cmd
}
