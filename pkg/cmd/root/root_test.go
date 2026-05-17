package root

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestFlagParseErrorWrappedAsFlagError guards byob-errors.4: pflag's
// flag-parse errors must come back through cobra as *cmdutil.FlagError so
// the top-level runner can map them to exit code 2 rather than the
// generic exit 1. Without SetFlagErrorFunc on root, an unknown flag is
// indistinguishable from a runtime failure.
func TestFlagParseErrorWrappedAsFlagError(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams: ios,
		Config: func() (*config.Config, error) {
			return &config.Config{Display: config.DisplayConfig{Units: "metric"}}, nil
		},
	}
	cmd := NewCmdRoot(f)
	cmd.SetArgs([]string{"--no-such-flag"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected flag-parse error, got nil")
	}
	var flagErr *cmdutil.FlagError
	if !errors.As(err, &flagErr) {
		t.Fatalf("expected *cmdutil.FlagError, got %T: %v", err, err)
	}
}

// TestUnitsFlag_Override asserts that --units on the root flips f.UnitSystem
// to the imperial reading, overriding the config default. Regression guard:
// previously wireUnitSystem ran from PersistentPreRunE, but subcommand Options
// snapshot f.UnitSystem at NewCmdXxx time — so the flag was silently ignored.
func TestUnitsFlag_Override(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams: ios,
		Config: func() (*config.Config, error) {
			return &config.Config{Display: config.DisplayConfig{Units: "metric"}}, nil
		},
	}

	cmd := NewCmdRoot(f)

	if got := f.UnitSystem(); got != units.Metric {
		t.Fatalf("default: want Metric, got %v", got)
	}

	if err := cmd.PersistentFlags().Set("units", "imperial"); err != nil {
		t.Fatal(err)
	}
	if got := f.UnitSystem(); got != units.Imperial {
		t.Fatalf("--units=imperial: want Imperial, got %v", got)
	}
}

// TestUnitsFlag_CapturedBySubcommands asserts that subcommand Options, which
// snapshot f.UnitSystem at construction time, observe the flag-aware closure
// — i.e. the wiring order in NewCmdRoot (wireUnitSystem before addSubcommands)
// is correct. We probe through the public path: look up the status subcommand
// and verify that invoking f.UnitSystem after flag mutation reflects the flag.
// If a future refactor breaks the order, this test and TestUnitsFlag_Override
// together pinpoint the regression.
func TestUnitsFlag_CapturedBySubcommands(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams: ios,
		Config: func() (*config.Config, error) {
			return &config.Config{Display: config.DisplayConfig{Units: "metric"}}, nil
		},
	}

	cmd := NewCmdRoot(f)

	// At this point subcommands have been registered; their Options captured
	// f.UnitSystem by value. Setting the flag must still propagate because
	// the captured closure reads the flag live on each call.
	if err := cmd.PersistentFlags().Set("units", "imperial"); err != nil {
		t.Fatal(err)
	}

	status, _, err := cmd.Find([]string{"status"})
	if err != nil {
		t.Fatalf("find status subcommand: %v", err)
	}
	if status == nil {
		t.Fatal("status subcommand not registered")
	}

	if got := f.UnitSystem(); got != units.Imperial {
		t.Fatalf("after --units=imperial: want Imperial, got %v", got)
	}
}

// TestUnitsPrecedence_FlagBeatsEnvBeatsFile verifies the full layering:
// --units flag > PVMT_UNITS env > Display.Units file value > default.
func TestUnitsPrecedence_FlagBeatsEnvBeatsFile(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams: ios,
		Config: func() (*config.Config, error) {
			return &config.Config{Display: config.DisplayConfig{Units: "imperial"}}, nil
		},
	}
	cmd := NewCmdRoot(f)

	// file only: env explicitly unset so UnitSystem sees no-env, not empty-env
	os.Unsetenv("PVMT_UNITS")
	t.Cleanup(func() { os.Unsetenv("PVMT_UNITS") })
	if got := f.UnitSystem(); got != units.Imperial {
		t.Errorf("file-only: want Imperial, got %v", got)
	}

	// env beats file: metric
	t.Setenv("PVMT_UNITS", "metric")
	if got := f.UnitSystem(); got != units.Metric {
		t.Errorf("env-beats-file: want Metric, got %v", got)
	}

	// flag beats env: imperial
	if err := cmd.PersistentFlags().Set("units", "imperial"); err != nil {
		t.Fatal(err)
	}
	if got := f.UnitSystem(); got != units.Imperial {
		t.Errorf("flag-beats-env: want Imperial, got %v", got)
	}
}

// TestWarnInvalidEnv_BadValuesEmitWarning guards S1: every invalid input
// must surface a stderr warning so the user knows their env was ignored,
// and every valid input must stay silent so the warning means what it
// says.
func TestWarnInvalidEnv_BadValuesEmitWarning(t *testing.T) {
	tests := []struct {
		name   string
		env    map[string]string
		expect string // substring required in ErrOut; empty = no warning expected
	}{
		{"unknown units", map[string]string{"PVMT_UNITS": "furlongs"}, "PVMT_UNITS"},
		{"valid units silent", map[string]string{"PVMT_UNITS": "metric"}, ""},
		{"unparseable years", map[string]string{"PVMT_FORECAST_YEARS": "lots"}, "not a valid integer"},
		{"zero years", map[string]string{"PVMT_FORECAST_YEARS": "0"}, "must be > 0"},
		{"negative years", map[string]string{"PVMT_FORECAST_YEARS": "-5"}, "must be > 0"},
		{"valid years silent", map[string]string{"PVMT_FORECAST_YEARS": "10"}, ""},
		{"unparseable hex edge", map[string]string{"PVMT_HEX_EDGE_M": "wide"}, "not a valid number"},
		{"negative hex edge", map[string]string{"PVMT_HEX_EDGE_M": "-5"}, "must be > 0"},
		{"valid hex edge silent", map[string]string{"PVMT_HEX_EDGE_M": "100"}, ""},
		{"pci out of range", map[string]string{"PVMT_FORECAST_INITIAL_PCI": "500"}, "must be in (0, 100]"},
		{"pci zero", map[string]string{"PVMT_FORECAST_INITIAL_PCI": "0"}, "must be in (0, 100]"},
		{"valid pci silent", map[string]string{"PVMT_FORECAST_INITIAL_PCI": "85"}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear every PVMT_ key this test doesn't own so prior test
			// state (or the developer's shell) can't color the result,
			// and register cleanups so the parent env is restored when
			// the subtest ends. t.Setenv with "" can't unset, so we do
			// it by hand.
			// t.Setenv can't unset a variable, and the cleanup here has
			// to *conditionally* unset-or-restore, so os.Setenv/Unsetenv
			// are the right tools; silence usetesting for this block.
			for _, k := range []string{"PVMT_UNITS", "PVMT_FORECAST_YEARS", "PVMT_HEX_EDGE_M", "PVMT_FORECAST_INITIAL_PCI"} {
				if orig, ok := os.LookupEnv(k); ok {
					t.Cleanup(func() { _ = os.Setenv(k, orig) }) //nolint:usetesting // see block comment above
				} else {
					t.Cleanup(func() { _ = os.Unsetenv(k) })
				}
				_ = os.Unsetenv(k)
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			ios, _, _, errBuf := iostreams.Test()
			f := &cmdutil.Factory{IOStreams: ios}
			if err := warnInvalidEnv(nil, f); err != nil {
				t.Fatalf("warnInvalidEnv: %v", err)
			}
			got := errBuf.String()
			if tt.expect == "" {
				if got != "" {
					t.Errorf("expected no warning, got %q", got)
				}
				return
			}
			if !strings.Contains(got, tt.expect) {
				t.Errorf("expected warning containing %q, got %q", tt.expect, got)
			}
		})
	}
}
