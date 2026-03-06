package root

import (
	"pvmt/pkg/cmd/all"
	"pvmt/pkg/cmd/parking"
	"pvmt/pkg/cmd/roads"
	"pvmt/pkg/cmd/serve"
	"pvmt/pkg/cmd/status"
	"pvmt/pkg/cmdutil"

	"github.com/spf13/cobra"
)

func NewCmdRoot(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pvmt",
		Short: "Pavement data ingestion & forecasting tool",
		Long:  "PVMT ingests pavement data (roads, parking lots) from OSM and ArcGIS APIs,\ncomputes paved area via geometry operations, and serves a MapLibre visualization.",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	// Resource commands
	cmd.AddGroup(&cobra.Group{ID: "resource", Title: "Resource commands:"})
	roadsCmd := roads.NewCmdRoads(f)
	roadsCmd.GroupID = "resource"
	parkingCmd := parking.NewCmdParking(f)
	parkingCmd.GroupID = "resource"
	allCmd := all.NewCmdAll(f)
	allCmd.GroupID = "resource"

	cmd.AddCommand(roadsCmd)
	cmd.AddCommand(parkingCmd)
	cmd.AddCommand(allCmd)

	// Server commands
	cmd.AddGroup(&cobra.Group{ID: "server", Title: "Server commands:"})
	serveCmd := serve.NewCmdServe(f)
	serveCmd.GroupID = "server"
	cmd.AddCommand(serveCmd)

	// Info commands
	cmd.AddGroup(&cobra.Group{ID: "info", Title: "Info commands:"})
	statusCmd := status.NewCmdStatus(f, nil, nil)
	statusCmd.GroupID = "info"
	cmd.AddCommand(statusCmd)

	return cmd
}
