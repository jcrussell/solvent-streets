package cmdutil

import (
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Source is the --source enum for the ingest subcommand. Implements pflag.Value
// so invalid values fail at parse time rather than inside the runFunc.
type Source string

const (
	SourceAll      Source = "all"
	SourceOverpass Source = "overpass"
	SourceArcGIS   Source = "arcgis"
)

var sourceValues = []string{"all", "overpass", "arcgis"}

func (s *Source) Set(v string) error {
	if !slices.Contains(sourceValues, v) {
		return FlagErrorf("unknown source %q, valid sources: %s", v, strings.Join(sourceValues, ", "))
	}
	*s = Source(v)
	return nil
}

func (s *Source) String() string { return string(*s) }
func (s *Source) Type() string   { return "source" }

// SourceCompletion returns the completion func for --source.
func SourceCompletion() cobra.CompletionFunc {
	return cobra.FixedCompletions(sourceValues, cobra.ShellCompDirectiveNoFileComp)
}

// UnitSystem is the --units enum for the root command. Empty is a legal value
// meaning "fall back to config"; non-empty values are validated against the
// allowlist at parse time.
type UnitSystem string

const (
	UnitMetric   UnitSystem = "metric"
	UnitImperial UnitSystem = "imperial"
)

func (u *UnitSystem) Set(v string) error {
	if v == "" {
		*u = ""
		return nil
	}
	switch v {
	case "metric", "imperial":
		*u = UnitSystem(v)
		return nil
	}
	return FlagErrorf("unknown unit system %q, valid: metric, imperial", v)
}

func (u *UnitSystem) String() string { return string(*u) }
func (u *UnitSystem) Type() string   { return "units" }

// UnitSystemCompletion returns the completion func for --units.
func UnitSystemCompletion() cobra.CompletionFunc {
	return cobra.FixedCompletions([]string{"metric", "imperial"}, cobra.ShellCompDirectiveNoFileComp)
}

// CitySlugCompletion reads pvmt.toml on tab-completion and returns configured
// city slugs. Degrades silently on any config load error so completion works
// outside a project tree.
func CitySlugCompletion(f *Factory) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		cfg, err := f.Config()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		out := make([]cobra.Completion, 0, len(cfg.Cities))
		for i := range cfg.Cities {
			slug := cfg.Cities[i].Slug()
			if strings.HasPrefix(slug, toComplete) {
				out = append(out, slug)
			}
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

// Compile-time assertions so a drift in pflag.Value fails here, not at
// a distant cmd.Flags().Var call site.
var (
	_ pflag.Value = (*Source)(nil)
	_ pflag.Value = (*UnitSystem)(nil)
)
