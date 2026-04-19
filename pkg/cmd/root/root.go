package root

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"pvmt/internal/units"
	"pvmt/pkg/cmd/all"
	"pvmt/pkg/cmd/cities"
	configcmd "pvmt/pkg/cmd/config"
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

// middleware applies cross-cutting setup once per invocation. Called from
// root.PersistentPreRunE after flag parsing, before any subcommand RunE.
// Not suitable for anything that subcommand Options snapshot from the
// Factory at NewCmdXxx time — those bindings must be in place before
// addSubcommands runs (see wireUnitSystem).
type middleware func(root *cobra.Command, f *cmdutil.Factory) error

var middlewares = []middleware{
	warnInvalidEnv,
}

// warnInvalidEnv emits a one-line stderr warning for any PVMT_* env var
// set to an unparseable or out-of-range value. The config resolvers
// (UnitSystem, HexEdge, ResolvedForecast) silently fall through on
// invalid input, which is safe but gives the user no signal their env
// was ignored; this middleware is the signal. Range checks mirror the
// validation inside those resolvers so a warning implies the env will
// be ignored and silence implies it will be honored.
func warnInvalidEnv(_ *cobra.Command, f *cmdutil.Factory) error {
	ios := f.IOStreams
	if ios == nil {
		return errors.New("warnInvalidEnv: factory has nil IOStreams")
	}
	warnf := func(format string, args ...any) {
		fmt.Fprintf(ios.ErrOut, "warning: "+format+"; falling back to config/default\n", args...)
	}
	if v, ok := os.LookupEnv("PVMT_UNITS"); ok && v != "" && !units.IsKnown(v) {
		warnf("PVMT_UNITS=%q is not a known unit system", v)
	}
	if v, ok := os.LookupEnv("PVMT_FORECAST_YEARS"); ok && v != "" {
		n, err := strconv.Atoi(v)
		switch {
		case err != nil:
			warnf("PVMT_FORECAST_YEARS=%q is not a valid integer", v)
		case n <= 0:
			warnf("PVMT_FORECAST_YEARS=%q must be > 0", v)
		}
	}
	if v, ok := os.LookupEnv("PVMT_HEX_EDGE_M"); ok && v != "" {
		n, err := strconv.ParseFloat(v, 64)
		switch {
		case err != nil:
			warnf("PVMT_HEX_EDGE_M=%q is not a valid number", v)
		case n <= 0:
			warnf("PVMT_HEX_EDGE_M=%q must be > 0", v)
		}
	}
	if v, ok := os.LookupEnv("PVMT_FORECAST_INITIAL_PCI"); ok && v != "" {
		n, err := strconv.ParseFloat(v, 64)
		switch {
		case err != nil:
			warnf("PVMT_FORECAST_INITIAL_PCI=%q is not a valid number", v)
		case n <= 0 || n > 100:
			warnf("PVMT_FORECAST_INITIAL_PCI=%q must be in (0, 100]", v)
		}
	}
	return nil
}

func NewCmdRoot(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "pvmt",
		Short:         "Pavement data ingestion & forecasting tool",
		Long:          "PVMT ingests pavement data (roads, parking lots) from OSM and ArcGIS APIs,\ncomputes paved area via geometry operations, and serves a MapLibre visualization.",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	cmdutil.AddCityOverride(cmd, f)
	var unitSystem cmdutil.UnitSystem
	cmd.PersistentFlags().Var(&unitSystem, "units", "Display units: metric or imperial (overrides config)")
	_ = cmd.RegisterFlagCompletionFunc("units", cmdutil.UnitSystemCompletion())

	// Must run before addSubcommands: subcommands snapshot f.UnitSystem
	// into their Options structs at construction time, and Go function
	// values are copied, not reference-tracked — so the flag-aware
	// closure must replace the factory default before the snapshot.
	wireUnitSystem(cmd, f)

	cmd.PersistentPreRunE = func(c *cobra.Command, args []string) error {
		if skipMiddleware(c) {
			return nil
		}
		for _, m := range middlewares {
			if err := m(cmd, f); err != nil {
				return err
			}
		}
		return nil
	}

	addCommandGroups(cmd)
	addSubcommands(cmd, f)

	return cmd
}

func skipMiddleware(c *cobra.Command) bool {
	switch c.Name() {
	case "help", "completion", "version":
		return true
	}
	if strings.HasPrefix(c.Name(), "__complete") {
		return true
	}
	if p := c.Parent(); p != nil && p.Name() == "completion" {
		return true
	}
	return false
}

func wireUnitSystem(root *cobra.Command, f *cmdutil.Factory) {
	f.UnitSystem = func() units.System {
		if fl := root.PersistentFlags().Lookup("units"); fl != nil && fl.Changed {
			return units.ParseSystem(fl.Value.String())
		}
		cfg, err := f.Config()
		if err != nil {
			return units.Imperial
		}
		return cfg.UnitSystem()
	}
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
	addGroupedCommand(cmd, roads.NewCmdRoads(f), groupResource)
	addGroupedCommand(cmd, parking.NewCmdParking(f), groupResource)
	addGroupedCommand(cmd, sidewalks.NewCmdSidewalks(f), groupResource)
	addGroupedCommand(cmd, all.NewCmdAll(f), groupResource)

	addGroupedCommand(cmd, serve.NewCmdServe(f, nil), groupServer)
	addGroupedCommand(cmd, export.NewCmdExport(f, nil), groupServer)

	addGroupedCommand(cmd, forecastcmd.NewCmdForecast(f, nil), groupAnalysis)

	addGroupedCommand(cmd, status.NewCmdStatus(f, nil, nil), groupInfo)
	addGroupedCommand(cmd, cities.NewCmdCities(f, nil), groupInfo)
	addGroupedCommand(cmd, configcmd.NewCmdConfig(f), groupInfo)
	addGroupedCommand(cmd, version.NewCmdVersion(f, nil), groupInfo)
}
