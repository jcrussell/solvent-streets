package config

import (
	"os"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/units"
)

// clearOverrideEnv unsets every override variable for the test, so the
// developer's shell cannot colour the result. t.Setenv registers the restore;
// it cannot itself unset.
func clearOverrideEnv(t *testing.T) {
	t.Helper()
	for _, r := range envRules {
		t.Setenv(r.name, "")
		_ = os.Unsetenv(r.name)
	}
}

// TestEnvProblems_ReportedExactlyWhenDropped pins the contract the CLI's
// warning rests on: for every override variable and value, EnvProblems names
// the variable exactly when the resolver does not apply it.
func TestEnvProblems_ReportedExactlyWhenDropped(t *testing.T) {
	// applied reports whether the resolver took its value from the env layer.
	applied := map[string]func(c *Config) bool{
		EnvUnits: func(c *Config) bool {
			_, src := c.resolveUnits("")
			return src.Kind == SourceEnv
		},
		EnvHexEdgeM: func(c *Config) bool {
			_, src := c.resolveHexEdge()
			_, citySrc := c.resolveHexEdgeForCity(&CityConfig{Name: "Test", HexEdgeM: 80})
			return src.Kind == SourceEnv && citySrc.Kind == SourceEnv
		},
		EnvForecastYears: func(c *Config) bool {
			_, prov := c.resolveForecast(nil)
			return prov.Years.Kind == SourceEnv
		},
		EnvForecastInitialPCI: func(c *Config) bool {
			_, prov := c.resolveForecast(nil)
			return prov.InitialPCI.Kind == SourceEnv
		},
	}
	cases := []struct {
		name, value string
		reason      string // "" = honored
	}{
		{EnvUnits, "metric", ""},
		{EnvUnits, "imperial", ""},
		{EnvUnits, "furlongs", "is not a known unit system"},

		{EnvHexEdgeM, "100", ""},
		{EnvHexEdgeM, "10", ""},
		{EnvHexEdgeM, "wide", "is not a valid number"},
		{EnvHexEdgeM, "inf", "must be a finite number"},
		{EnvHexEdgeM, "nan", "must be a finite number"},
		{EnvHexEdgeM, "9.99", "must be >= 10 (hex count grows as 1/edge²)"},
		{EnvHexEdgeM, "-5", "must be >= 10 (hex count grows as 1/edge²)"},

		{EnvForecastYears, "10", ""},
		{EnvForecastYears, "200", ""},
		{EnvForecastYears, "lots", "is not a valid integer"},
		{EnvForecastYears, "12.5", "is not a valid integer"},
		{EnvForecastYears, "0", "must be between 1 and 200"},
		{EnvForecastYears, "-5", "must be between 1 and 200"},
		{EnvForecastYears, "201", "must be between 1 and 200"},

		{EnvForecastInitialPCI, "85", ""},
		{EnvForecastInitialPCI, "100", ""},
		{EnvForecastInitialPCI, "0.5", ""},
		{EnvForecastInitialPCI, "good", "is not a valid number"},
		{EnvForecastInitialPCI, "nan", "must be a finite number"},
		{EnvForecastInitialPCI, "-inf", "must be a finite number"},
		{EnvForecastInitialPCI, "0", "must be in (0, 100]"},
		{EnvForecastInitialPCI, "500", "must be in (0, 100]"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			clearOverrideEnv(t)
			t.Setenv(tc.name, tc.value)

			problems := EnvProblems()
			if tc.reason == "" {
				if len(problems) != 0 {
					t.Errorf("EnvProblems() = %+v, want none", problems)
				}
			} else if len(problems) != 1 || problems[0] != (EnvProblem{Variable: tc.name, Value: tc.value, Reason: tc.reason}) {
				t.Errorf("EnvProblems() = %+v, want one: %s=%q %s", problems, tc.name, tc.value, tc.reason)
			}
			if got, want := applied[tc.name](&Config{}), tc.reason == ""; got != want {
				t.Errorf("resolver applied the env value = %v, want %v", got, want)
			}
		})
	}
}

// TestEnvProblems_UnsetAndEmptyAreSilent: an empty variable is no override
// and no problem.
func TestEnvProblems_UnsetAndEmptyAreSilent(t *testing.T) {
	clearOverrideEnv(t)
	if p := EnvProblems(); len(p) != 0 {
		t.Errorf("unset: EnvProblems() = %+v, want none", p)
	}
	for _, r := range envRules {
		t.Setenv(r.name, "")
	}
	if p := EnvProblems(); len(p) != 0 {
		t.Errorf("empty: EnvProblems() = %+v, want none", p)
	}
	c := &Config{}
	if v, src := c.resolveUnits(""); v != units.Imperial || src.Kind != SourceDefault {
		t.Errorf("empty PVMT_UNITS resolved to %v from %v, want the default", v, src)
	}
}

// TestEnvRules_CoverEveryOverride keeps the rule table in step with the
// variables the package names: each Env* constant has exactly one rule, and
// each is a PVMT_ variable.
func TestEnvRules_CoverEveryOverride(t *testing.T) {
	want := map[string]bool{EnvUnits: true, EnvHexEdgeM: true, EnvForecastYears: true, EnvForecastInitialPCI: true}
	for _, r := range envRules {
		if !strings.HasPrefix(r.name, "PVMT_") {
			t.Errorf("rule %q is not a PVMT_ variable", r.name)
		}
		if !want[r.name] {
			t.Errorf("rule %q is duplicated or has no Env constant", r.name)
		}
		delete(want, r.name)
	}
	for name := range want {
		t.Errorf("%s has no rule in envRules", name)
	}
}
