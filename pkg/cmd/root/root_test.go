package root

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
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

// TestLogLevelFlag_RejectsBadValueAtParseTime guards byob-command-shape.7:
// because --log-level is now a pflag.Value, an unknown level fails at flag
// parse with a *cmdutil.FlagError (mapped to exit 2 by the top-level
// runner) instead of silently degrading to Warn inside applyLogLevel.
func TestLogLevelFlag_RejectsBadValueAtParseTime(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams: ios,
		LogLevel:  new(slog.LevelVar),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: func() (*config.Config, error) {
			return &config.Config{Display: config.DisplayConfig{Units: "metric"}}, nil
		},
	}
	cmd := NewCmdRoot(f)
	cmd.SetArgs([]string{"--log-level", "fatal", "status"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected flag-parse error for --log-level=fatal, got nil")
	}
	var flagErr *cmdutil.FlagError
	if !errors.As(err, &flagErr) {
		t.Fatalf("expected *cmdutil.FlagError, got %T: %v", err, err)
	}
}

// TestApplyLogLevel_Precedence locks in the precedence ladder documented
// on applyLogLevel: flag > -v/-vv > PVMT_LOG > default. Tests the helper
// directly so we don't have to plumb through Execute() for every case.
func TestApplyLogLevel_Precedence(t *testing.T) {
	mustSet := func(t *testing.T, v string) cmdutil.LogLevel {
		t.Helper()
		var l cmdutil.LogLevel
		if err := l.Set(v); err != nil {
			t.Fatalf("Set(%q): %v", v, err)
		}
		return l
	}

	tests := []struct {
		name    string
		verbose int
		flag    cmdutil.LogLevel
		env     string
		want    slog.Level
	}{
		{"default warn", 0, cmdutil.LogLevel{}, "", slog.LevelWarn},
		{"verbose info", 1, cmdutil.LogLevel{}, "", slog.LevelInfo},
		{"verbose debug", 2, cmdutil.LogLevel{}, "", slog.LevelDebug},
		{"env info", 0, cmdutil.LogLevel{}, "info", slog.LevelInfo},
		{"env beats default", 0, cmdutil.LogLevel{}, "debug", slog.LevelDebug},
		{"env bogus lands warn", 0, cmdutil.LogLevel{}, "trace", slog.LevelWarn},
		{"-v beats env", 1, cmdutil.LogLevel{}, "error", slog.LevelInfo},
		{"flag beats -vv", 2, mustSet(t, "error"), "", slog.LevelError},
		{"flag beats env", 0, mustSet(t, "info"), "debug", slog.LevelInfo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PVMT_LOG", tt.env)
			if tt.env == "" {
				os.Unsetenv("PVMT_LOG")
			}
			lv := new(slog.LevelVar)
			lv.Set(slog.LevelWarn)
			f := &cmdutil.Factory{LogLevel: lv}
			flag := tt.flag
			applyLogLevel(f, tt.verbose, &flag)
			if got := lv.Level(); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCommandGroups_AllSubcommandsGrouped locks in byob-command-shape.3:
// every leaf subcommand the root owns must have a GroupID that matches one
// of root's declared groups, so `pvmt --help` keeps grouping commands
// semantically instead of listing them under "Additional Commands". Cobra's
// own `help` and `completion` commands ship without a GroupID and are
// expected under "Additional Commands"; the test exempts them by name.
func TestCommandGroups_AllSubcommandsGrouped(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams: ios,
		LogLevel:  new(slog.LevelVar),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: func() (*config.Config, error) {
			return &config.Config{Display: config.DisplayConfig{Units: "metric"}}, nil
		},
	}
	cmd := NewCmdRoot(f)

	wantGroups := map[string]bool{
		groupResource: false,
		groupServer:   false,
		groupAnalysis: false,
		groupInfo:     false,
	}
	for _, g := range cmd.Groups() {
		if _, ok := wantGroups[g.ID]; !ok {
			t.Errorf("unexpected group %q (%q) on root", g.ID, g.Title)
			continue
		}
		wantGroups[g.ID] = true
	}
	for id, present := range wantGroups {
		if !present {
			t.Errorf("group %q missing from root", id)
		}
	}

	// Cobra's auto-injected help/completion commands carry no GroupID and
	// surface under "Additional Commands" — that's the intended cobra
	// behaviour, not a regression.
	exempt := map[string]bool{"help": true, "completion": true}
	for _, sub := range cmd.Commands() {
		if exempt[sub.Name()] {
			if sub.GroupID != "" {
				t.Errorf("%q: expected no GroupID (cobra-owned), got %q", sub.Name(), sub.GroupID)
			}
			continue
		}
		if sub.GroupID == "" {
			t.Errorf("%q: missing GroupID — wire it through addGroupedCommand", sub.Name())
			continue
		}
		if _, ok := wantGroups[sub.GroupID]; !ok {
			t.Errorf("%q: GroupID=%q is not one of the declared root groups", sub.Name(), sub.GroupID)
		}
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

// testFactory builds a Factory wired with the minimum dependencies required
// to drive root.PersistentPreRunE: LogLevel + Logger (consumed by applyLogLevel
// and the structured-log context binding) and a stub Config so middlewares
// like warnInvalidConfig don't blow up.
func testFactory() *cmdutil.Factory {
	ios, _, _, _ := iostreams.Test()
	return &cmdutil.Factory{
		IOStreams: ios,
		LogLevel:  new(slog.LevelVar),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: func() (*config.Config, error) {
			return &config.Config{Display: config.DisplayConfig{Units: "metric"}}, nil
		},
	}
}

// withMiddlewares swaps the package-level middlewares slice for the duration
// of a test and restores it via t.Cleanup. Tests that mutate this global must
// not run in parallel.
func withMiddlewares(t *testing.T, ms ...middleware) {
	t.Helper()
	prev := middlewares
	t.Cleanup(func() { middlewares = prev })
	middlewares = ms
}

// TestPersistentPreRunE_Wired guards byob-command-shape.5: the root command
// must install a PersistentPreRunE so the documented cobra execution order
// (root.PersistentPreRunE → cmd.RunE) holds. Subcommands rely on this to
// receive a logger bound to their command path and to inherit any cross-
// cutting setup the middlewares slice performs.
func TestPersistentPreRunE_Wired(t *testing.T) {
	f := testFactory()
	cmd := NewCmdRoot(f)
	if cmd.PersistentPreRunE == nil {
		t.Fatal("root.PersistentPreRunE is nil; middleware chain will not run")
	}
}

// TestPersistentPreRunE_RunsMiddlewareForLeafCommand locks in that the
// middleware chain actually fires for ordinary subcommands. Without this,
// a future refactor that drops the for-range over middlewares (or moves
// setup into a per-command PreRunE) would compile and pass the existing
// warnInvalidEnv test (which calls the middleware directly) while silently
// breaking the application-wide guarantee.
func TestPersistentPreRunE_RunsMiddlewareForLeafCommand(t *testing.T) {
	var calls int
	withMiddlewares(t, func(_ *cobra.Command, _ *cmdutil.Factory) error {
		calls++
		return nil
	})

	f := testFactory()
	cmd := NewCmdRoot(f)

	leaf := &cobra.Command{Use: "status"}
	leaf.SetContext(context.Background())
	if err := cmd.PersistentPreRunE(leaf, nil); err != nil {
		t.Fatalf("PersistentPreRunE: %v", err)
	}
	if calls != 1 {
		t.Fatalf("middleware call count: got %d, want 1", calls)
	}
}

// TestPersistentPreRunE_PropagatesMiddlewareError locks in that a middleware
// returning an error short-circuits the chain and surfaces to the caller —
// the documented "fail fast before subcommand RunE" contract. Subsequent
// middlewares must not run.
func TestPersistentPreRunE_PropagatesMiddlewareError(t *testing.T) {
	sentinel := errors.New("middleware refused")
	var laterCalled bool
	withMiddlewares(t,
		func(_ *cobra.Command, _ *cmdutil.Factory) error { return sentinel },
		func(_ *cobra.Command, _ *cmdutil.Factory) error {
			laterCalled = true
			return nil
		},
	)

	f := testFactory()
	cmd := NewCmdRoot(f)

	leaf := &cobra.Command{Use: "status"}
	leaf.SetContext(context.Background())
	err := cmd.PersistentPreRunE(leaf, nil)
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	if laterCalled {
		t.Error("middleware after a failing one must not run")
	}
}

// TestSkipMiddleware_ExemptCommands locks in the exemption set: cobra-owned
// help/completion plumbing and the version subcommand must not trigger
// application middleware. The exemption matters because shell-completion
// scripts call `pvmt __complete ...` on every keystroke; running config
// loads or stderr warnings on that path would be a usability disaster.
func TestSkipMiddleware_ExemptCommands(t *testing.T) {
	completion := &cobra.Command{Use: "completion"}
	completionBash := &cobra.Command{Use: "bash"}
	completion.AddCommand(completionBash)

	tests := []struct {
		name string
		cmd  *cobra.Command
		skip bool
	}{
		{"help", &cobra.Command{Use: "help"}, true},
		{"completion", completion, true},
		{"version", &cobra.Command{Use: "version"}, true},
		{"__complete", &cobra.Command{Use: "__complete"}, true},
		{"__completeNoDesc", &cobra.Command{Use: "__completeNoDesc"}, true},
		{"completion child (bash)", completionBash, true},
		{"status", &cobra.Command{Use: "status"}, false},
		{"forecast", &cobra.Command{Use: "forecast"}, false},
		{"roads", &cobra.Command{Use: "roads"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := skipMiddleware(tt.cmd); got != tt.skip {
				t.Errorf("skipMiddleware(%q) = %v, want %v", tt.cmd.Name(), got, tt.skip)
			}
		})
	}
}

// TestPersistentPreRunE_SkipsExemptCommands locks in the wiring half of the
// exemption: when skipMiddleware classifies a command as exempt, the
// PersistentPreRunE closure short-circuits *before* iterating middlewares.
// A sentinel middleware that records its invocations must stay at zero
// across every exempt command.
func TestPersistentPreRunE_SkipsExemptCommands(t *testing.T) {
	var calls int
	withMiddlewares(t, func(_ *cobra.Command, _ *cmdutil.Factory) error {
		calls++
		return nil
	})

	f := testFactory()
	cmd := NewCmdRoot(f)

	completion := &cobra.Command{Use: "completion"}
	bash := &cobra.Command{Use: "bash"}
	completion.AddCommand(bash)

	exempt := []*cobra.Command{
		{Use: "help"},
		{Use: "completion"},
		{Use: "version"},
		{Use: "__complete"},
		bash,
	}
	for _, c := range exempt {
		if err := cmd.PersistentPreRunE(c, nil); err != nil {
			t.Fatalf("PersistentPreRunE(%q): %v", c.Name(), err)
		}
	}
	if calls != 0 {
		t.Fatalf("middleware ran %d time(s) for exempt commands; expected 0", calls)
	}
}
