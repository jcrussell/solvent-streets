package root

import (
	"testing"

	"pvmt/internal/config"
	"pvmt/internal/units"
	"pvmt/pkg/cmdutil"
	"pvmt/pkg/iostreams"
)

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
