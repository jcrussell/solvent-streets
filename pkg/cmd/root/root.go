package root

import (
	"pvmt/internal/units"
	"pvmt/pkg/cmd/all"
	"pvmt/pkg/cmd/cities"
	"pvmt/pkg/cmd/export"
	forecastcmd "pvmt/pkg/cmd/forecast"
	"pvmt/pkg/cmd/parking"
	"pvmt/pkg/cmd/roads"
	"pvmt/pkg/cmd/serve"
	"pvmt/pkg/cmd/sidewalks"
	"pvmt/pkg/cmd/status"
	"pvmt/pkg/cmd/version"
	"pvmt/pkg/cmdutil"

	"github.com/spf13/cobra"
)

func NewCmdRoot(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "pvmt",
		Short:         "Pavement data ingestion & forecasting tool",
		Long:          "PVMT ingests pavement data (roads, parking lots) from OSM and ArcGIS APIs,\ncomputes paved area via geometry operations, and serves a MapLibre visualization.",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	// Register --city/-c flag on root, overriding CurrentCity
	cmdutil.AddCityOverride(cmd, f)

	// Register --units flag on root, overriding display unit system
	cmd.PersistentFlags().String("units", "", "Display units: metric or imperial (overrides config)")
	f.UnitSystem = func() units.System {
		if fl := cmd.PersistentFlags().Lookup("units"); fl != nil && fl.Changed {
			return units.ParseSystem(fl.Value.String())
		}
		cfg, err := f.Config()
		if err != nil {
			return units.Imperial
		}
		return cfg.UnitSystem()
	}

	addCommandGroups(cmd)
	addSubcommands(cmd, f)

	return cmd
}

const (
	groupResource = "resource"
	groupServer   = "server"
	groupAnalysis = "analysis"
	groupInfo     = "info"
)

func addCommandGroups(cmd *cobra.Command) {
	cmd.AddGroup(&cobra.Group{ID: groupResource, Title: "Resource commands:"})
	cmd.AddGroup(&cobra.Group{ID: groupServer, Title: "Server commands:"})
	cmd.AddGroup(&cobra.Group{ID: groupAnalysis, Title: "Analysis commands:"})
	cmd.AddGroup(&cobra.Group{ID: groupInfo, Title: "Info commands:"})
}

func addGroupedCommand(cmd *cobra.Command, sub *cobra.Command, group string) {
	sub.GroupID = group
	cmd.AddCommand(sub)
}

func addSubcommands(cmd *cobra.Command, f *cmdutil.Factory) {
	// Resource commands
	addGroupedCommand(cmd, roads.NewCmdRoads(f), groupResource)
	addGroupedCommand(cmd, parking.NewCmdParking(f), groupResource)
	addGroupedCommand(cmd, sidewalks.NewCmdSidewalks(f), groupResource)
	addGroupedCommand(cmd, all.NewCmdAll(f), groupResource)

	// Server commands
	addGroupedCommand(cmd, serve.NewCmdServe(f, nil), groupServer)
	addGroupedCommand(cmd, export.NewCmdExport(f, nil), groupServer)

	// Analysis commands
	addGroupedCommand(cmd, forecastcmd.NewCmdForecast(f, nil), groupAnalysis)

	// Info commands
	addGroupedCommand(cmd, status.NewCmdStatus(f, nil, nil), groupInfo)
	addGroupedCommand(cmd, cities.NewCmdCities(f, nil), groupInfo)
	addGroupedCommand(cmd, version.NewCmdVersion(f, nil), groupInfo)
}
