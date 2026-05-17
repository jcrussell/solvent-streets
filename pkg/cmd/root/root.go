package root

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/jcrussell/solvent-streets/internal/build"
	"github.com/jcrussell/solvent-streets/internal/logs"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmd/all"
	"github.com/jcrussell/solvent-streets/pkg/cmd/cities"
	configcmd "github.com/jcrussell/solvent-streets/pkg/cmd/config"
	"github.com/jcrussell/solvent-streets/pkg/cmd/export"
	forecastcmd "github.com/jcrussell/solvent-streets/pkg/cmd/forecast"
	"github.com/jcrussell/solvent-streets/pkg/cmd/parking"
	"github.com/jcrussell/solvent-streets/pkg/cmd/roads"
	"github.com/jcrussell/solvent-streets/pkg/cmd/serve"
	"github.com/jcrussell/solvent-streets/pkg/cmd/sidewalks"
	"github.com/jcrussell/solvent-streets/pkg/cmd/status"
	"github.com/jcrussell/solvent-streets/pkg/cmd/version"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"

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
	warnInvalidConfig,
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

// warnInvalidConfig mirrors warnInvalidEnv for values loaded from
// pvmt.toml. resolveUnits silently falls through on unknown
// display.units (e.g. "metres" instead of "metric"); without this
// warning the user sees no signal their config string was ignored.
// Skipped when no config file is present (so `pvmt --help` works in
// any directory) and when config can't be loaded (the command path
// will surface that error). Discarding the error here is deliberate.
func warnInvalidConfig(_ *cobra.Command, f *cmdutil.Factory) error {
	cfg, _ := f.Config()
	if cfg == nil {
		return nil
	}
	if v := cfg.Display.Units; v != "" && !units.IsKnown(v) {
		cmdutil.Warnf(f.IOStreams, "display.units=%q is not a known unit system; falling back to default", v)
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
		Version:       build.Current().Short(),
	}
	cmd.SetVersionTemplate("pvmt {{.Version}}\n")

	// Wrap pflag's flag-parse errors as *FlagError so the top-level runner
	// maps them to exit code 2. Without this, "unknown flag" / "missing
	// argument" come back as plain errors and exit 1, indistinguishable
	// from runtime failures. Pairs with SilenceErrors=true above: the
	// runner owns formatting. (byob-errors.4)
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &cmdutil.FlagError{Err: err}
	})

	cmdutil.AddCityOverride(cmd, f)
	var unitSystem cmdutil.UnitSystem
	cmd.PersistentFlags().Var(&unitSystem, "units", "Display units: metric or imperial (overrides config)")
	_ = cmd.RegisterFlagCompletionFunc("units", cmdutil.UnitSystemCompletion())

	var verbose int
	var logLevel string
	cmd.PersistentFlags().CountVarP(&verbose, "verbose", "v", "increase log verbosity (-v=info, -vv=debug)")
	cmd.PersistentFlags().StringVar(&logLevel, "log-level", "", "explicit log level (warn|info|debug); overrides -v")

	// Must run before addSubcommands: subcommands snapshot f.UnitSystem
	// into their Options structs at construction time, and Go function
	// values are copied, not reference-tracked — so the flag-aware
	// closure must replace the factory default before the snapshot.
	wireUnitSystem(cmd, f)

	cmd.PersistentPreRunE = func(c *cobra.Command, args []string) error {
		if skipMiddleware(c) {
			return nil
		}
		applyLogLevel(f, verbose, logLevel)
		c.SetContext(logs.WithLogger(c.Context(), f.Logger.With("cmd", c.CommandPath())))
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

// applyLogLevel implements byob-logging.3's precedence ladder:
//
//	--log-level   (explicit; wins when set)
//	-v / -vv      (Info / Debug; loses to --log-level)
//	PVMT_LOG=...  (env var; loses to both flags, beats the default)
//	default       (Warn)
//
// Direction is deliberately opposite to byob-config.2's env > file > default:
// logging verbosity is per-invocation, so a -vv on the command line must
// not be silenced by a stale PVMT_LOG=warn in the environment.
func applyLogLevel(f *cmdutil.Factory, verbose int, logLevel string) {
	if f == nil || f.LogLevel == nil {
		return
	}
	switch {
	case logLevel != "":
		f.LogLevel.Set(parseLogLevel(logLevel))
	case verbose >= 2:
		f.LogLevel.Set(slog.LevelDebug)
	case verbose == 1:
		f.LogLevel.Set(slog.LevelInfo)
	case os.Getenv("PVMT_LOG") != "":
		f.LogLevel.Set(parseLogLevel(os.Getenv("PVMT_LOG")))
	}
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "error":
		return slog.LevelError
	default:
		return slog.LevelWarn
	}
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
