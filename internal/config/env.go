package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/jcrussell/solvent-streets/internal/forecast"
	"github.com/jcrussell/solvent-streets/internal/units"
)

// The environment variables that override a config value. Each is the env
// layer of the precedence chain in provenance.go.
const (
	EnvUnits                = "PVMT_UNITS"
	EnvHexEdgeM             = "PVMT_HEX_EDGE_M"
	EnvForecastHorizonYears = "PVMT_FORECAST_HORIZON_YEARS"
	EnvForecastInitialPCI   = "PVMT_FORECAST_INITIAL_PCI"
	EnvOverpassURL          = "PVMT_OVERPASS_URL" //nolint:gosec // G101: names a variable holding a URL, not a credential

	// envForecastYearsRenamed is the old name of EnvForecastHorizonYears. It
	// is never applied; EnvProblems reports it so the user hears why.
	envForecastYearsRenamed = "PVMT_FORECAST_YEARS"
)

// The env layer never passes through Validate, so each parse function below
// is the whole rule for its variable: the bound the file layer is validated
// against is applied here. A value a parse function refuses is dropped, the
// next layer down supplies the setting, and the refusal's reason is what
// EnvProblems reports. Resolution and reporting read the same function, so a
// value is reported exactly when it is dropped.
//
// Each returns the parsed value and "" when s is usable, or a reason that
// completes the sentence `PVMT_X="s" ...`.

func parseUnitsEnv(s string) (units.System, string) {
	if !units.IsKnown(s) {
		return units.Imperial, "is not a known unit system"
	}
	return units.ParseSystem(s), ""
}

// parseHexEdgeEnv enforces the MinHexEdgeM floor, the only thing standing
// between PVMT_HEX_EDGE_M=1 and an out-of-memory kill. The non-finite arm is
// its own reason and comes first: strconv.ParseFloat accepts "inf" with a nil
// error and +Inf passes the floor, and an infinite edge reaches geo.HexGrid,
// which returns no hexes, so the city exports an empty hex layer with exit 0.
func parseHexEdgeEnv(s string) (float64, string) {
	f, err := strconv.ParseFloat(s, 64)
	switch {
	case err != nil:
		return 0, "is not a valid number"
	case nonFinite(f):
		return 0, "must be a finite number"
	case f < MinHexEdgeM:
		return 0, fmt.Sprintf("must be >= %g (hex count grows as 1/edge²)", MinHexEdgeM)
	}
	return f, ""
}

// parseHorizonYearsEnv holds the horizon to forecast.MaxHorizonYears, the
// bound forecast.horizon_years is validated against.
func parseHorizonYearsEnv(s string) (int, string) {
	n, err := strconv.Atoi(s)
	switch {
	case err != nil:
		return 0, "is not a valid integer"
	case n <= 0 || n > forecast.MaxHorizonYears:
		return 0, fmt.Sprintf("must be between 1 and %d", forecast.MaxHorizonYears)
	}
	return n, ""
}

// parseInitialPCIEnv accepts a starting PCI (validInitialPCI). ParseFloat
// accepts "nan" with a nil error; an accepted NaN prints "Initial PCI: NaN"
// and every dollar column as NaN with exit code 0.
func parseInitialPCIEnv(s string) (float64, string) {
	f, err := strconv.ParseFloat(s, 64)
	switch {
	case err != nil:
		return 0, "is not a valid number"
	case nonFinite(f):
		return 0, "must be a finite number"
	case !validInitialPCI(f):
		return 0, fmt.Sprintf("must be in (0, %g]", forecast.MaxPCI)
	}
	return f, ""
}

// envOverride reads the override variable name through parse. ok reports a
// usable value; an unset or empty variable, or one parse refuses, is no
// override.
func envOverride[T any](name string, parse func(string) (T, string)) (v T, ok bool) {
	s, set := os.LookupEnv(name)
	if !set || s == "" {
		return v, false
	}
	v, reason := parse(s)
	return v, reason == ""
}

// reasonOf adapts a parse function to the part EnvProblems needs.
func reasonOf[T any](parse func(string) (T, string)) func(string) string {
	return func(s string) string {
		_, reason := parse(s)
		return reason
	}
}

// envRules lists every override variable with its rule, in the order
// EnvProblems reports them.
var envRules = []struct {
	name   string
	reason func(string) string
	// renamed marks an old variable name, which is never applied.
	renamed bool
}{
	{EnvUnits, reasonOf(parseUnitsEnv), false},
	{EnvForecastHorizonYears, reasonOf(parseHorizonYearsEnv), false},
	{envForecastYearsRenamed, func(string) string { return "is ignored: it was renamed to " + EnvForecastHorizonYears }, true},
	{EnvHexEdgeM, reasonOf(parseHexEdgeEnv), false},
	{EnvForecastInitialPCI, reasonOf(parseInitialPCIEnv), false},
	{EnvOverpassURL, reasonOf(parseOverpassURLEnv), false},
}

// EnvProblem is an override variable whose value the resolvers drop.
type EnvProblem struct {
	Variable string
	Value    string
	// Reason completes the sentence `Variable="Value" ...`.
	Reason string
	// Renamed reports an old variable name. Its value is never applied, and
	// the setting comes from the new name when that is set, so the warning
	// names no fallback layer.
	Renamed bool
}

// EnvProblems returns the override variables that are set to a value the
// resolvers drop, with the reason. The resolvers fall through to the next
// layer without a word, so this is how the CLI tells the user their
// environment was ignored: a variable listed here is not applied, and one not
// listed is.
func EnvProblems() []EnvProblem {
	var problems []EnvProblem
	for _, r := range envRules {
		s, set := os.LookupEnv(r.name)
		if !set || s == "" {
			continue
		}
		if reason := r.reason(s); reason != "" {
			problems = append(problems, EnvProblem{Variable: r.name, Value: s, Reason: reason, Renamed: r.renamed})
		}
	}
	return problems
}
