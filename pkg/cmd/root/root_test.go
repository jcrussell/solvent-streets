package root

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/goldentest"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

// TestFlagParseErrorWrappedAsFlagError: pflag's
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
	if _, ok := errors.AsType[*cmdutil.FlagError](err); !ok {
		t.Fatalf("expected *cmdutil.FlagError, got %T: %v", err, err)
	}
}

// TestUnitsFlag_Override asserts that --units on the root flips f.UnitSystem
// to the imperial reading, overriding the config default. Subcommand Options
// snapshot f.UnitSystem at NewCmdXxx time, so wiring the flag from
// PersistentPreRunE would silently ignore it.
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

// TestLogLevelFlag_RejectsBadValueAtParseTime:
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
	if _, ok := errors.AsType[*cmdutil.FlagError](err); !ok {
		t.Fatalf("expected *cmdutil.FlagError, got %T: %v", err, err)
	}
}

// TestApplyLogLevel_Precedence locks in the precedence ladder documented
// on applyLogLevel: flag > -v/-vv > PVMT_LOG_LEVEL > default. Tests the helper
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
			t.Setenv(EnvLogLevel, tt.env)
			if tt.env == "" {
				os.Unsetenv(EnvLogLevel)
			}
			t.Setenv(EnvLogLevelShort, "")
			os.Unsetenv(EnvLogLevelShort)
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

// TestApplyLogLevel_ShortEnv pins PVMT_LOG as the accepted short spelling of
// PVMT_LOG_LEVEL, and PVMT_LOG_LEVEL winning when both are set.
func TestApplyLogLevel_ShortEnv(t *testing.T) {
	tests := []struct {
		name        string
		long, short string
		want        slog.Level
	}{
		{"short alone", "", "debug", slog.LevelDebug},
		{"long beats short", "error", "debug", slog.LevelError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvLogLevel, tt.long)
			t.Setenv(EnvLogLevelShort, tt.short)
			lv := new(slog.LevelVar)
			lv.Set(slog.LevelWarn)
			applyLogLevel(&cmdutil.Factory{LogLevel: lv}, 0, &cmdutil.LogLevel{})
			if got := lv.Level(); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCommandGroups_AllSubcommandsGrouped:
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
		groupResource:    false,
		groupPublish:     false,
		groupAnalysis:    false,
		groupMaintenance: false,
		groupInfo:        false,
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
		{"unparseable years", map[string]string{"PVMT_FORECAST_HORIZON_YEARS": "lots"}, "not a valid integer"},
		{"zero years", map[string]string{"PVMT_FORECAST_HORIZON_YEARS": "0"}, "must be between 1 and 200"},
		{"negative years", map[string]string{"PVMT_FORECAST_HORIZON_YEARS": "-5"}, "must be between 1 and 200"},
		// The horizon bound a pvmt.toml is held to: a longer forecast is one
		// the browser refuses to run.
		{"years over the maximum", map[string]string{"PVMT_FORECAST_HORIZON_YEARS": "201"}, "must be between 1 and 200"},
		{"maximum years silent", map[string]string{"PVMT_FORECAST_HORIZON_YEARS": "200"}, ""},
		{"valid years silent", map[string]string{"PVMT_FORECAST_HORIZON_YEARS": "10"}, ""},
		{"unparseable hex edge", map[string]string{"PVMT_HEX_EDGE_M": "wide"}, "not a valid number"},
		{"negative hex edge", map[string]string{"PVMT_HEX_EDGE_M": "-5"}, "must be >="},
		// A positive-but-tiny edge is the decimal-point
		// typo that OOMs mid-compute, so it must warn like any other dropped
		// value -- hexEdgeFromEnv refuses it, and silence here would break the
		// silence-implies-honored contract above.
		{"below-floor hex edge", map[string]string{"PVMT_HEX_EDGE_M": "1"}, "must be >="},
		// Exactly the floor is honored, pinning the `>=` in hexEdgeFromEnv
		// against an off-by-one drift into `>`.
		{"floor hex edge silent", map[string]string{"PVMT_HEX_EDGE_M": "10"}, ""},
		{"valid hex edge silent", map[string]string{"PVMT_HEX_EDGE_M": "100"}, ""},
		{"pci out of range", map[string]string{"PVMT_FORECAST_INITIAL_PCI": "500"}, "must be in (0, 100]"},
		{"pci zero", map[string]string{"PVMT_FORECAST_INITIAL_PCI": "0"}, "must be in (0, 100]"},
		{"valid pci silent", map[string]string{"PVMT_FORECAST_INITIAL_PCI": "85"}, ""},
		// The old horizon variable names its replacement and no fallback:
		// the new variable, set here, is the one that applies.
		{"renamed years variable", map[string]string{"PVMT_FORECAST_YEARS": "20", "PVMT_FORECAST_HORIZON_YEARS": "25"}, `PVMT_FORECAST_YEARS="20" is ignored: it was renamed to PVMT_FORECAST_HORIZON_YEARS` + "\n"},
		{"invalid log level", map[string]string{"PVMT_LOG_LEVEL": "trace"}, "PVMT_LOG_LEVEL="},
		{"invalid short log level", map[string]string{"PVMT_LOG": "trace"}, "PVMT_LOG="},
		{"valid long log level silent", map[string]string{"PVMT_LOG_LEVEL": "info"}, ""},
		{"valid log level silent", map[string]string{"PVMT_LOG": "debug"}, ""},
		{"valid log level warning silent", map[string]string{"PVMT_LOG": "warning"}, ""},
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
			for _, k := range []string{"PVMT_UNITS", "PVMT_FORECAST_HORIZON_YEARS", "PVMT_HEX_EDGE_M", "PVMT_FORECAST_INITIAL_PCI", "PVMT_LOG", "PVMT_LOG_LEVEL", "PVMT_FORECAST_YEARS"} {
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

// TestMiddlewareChain_DoesNotDereferenceConfig guards lazy config at the
// place it is easiest to break: the chain runs for every non-exempt command,
// so one middleware calling f.Config() makes config discovery eager for all of
// them — `pvmt cache prune` would walk the filesystem and parse the whole
// include tree just to decide whether to print something. The [[include]]
// load warnings are emitted from the lazy Config closure in pkg/cmd/factory
// precisely so this stays true; a future middleware that reads the config must
// come with a different answer to this test, not a deletion of it.
func TestMiddlewareChain_DoesNotDereferenceConfig(t *testing.T) {
	var calls int
	f := testFactory()
	f.Config = func() (*config.Config, error) {
		calls++
		// A broken pvmt.toml, i.e. the case where eager loading hurts most:
		// a config-free command must still run to completion.
		return nil, errors.New("synthetic broken pvmt.toml")
	}

	cmd := NewCmdRoot(f)
	leaf := &cobra.Command{Use: "cache"}
	leaf.SetContext(context.Background())
	if err := cmd.PersistentPreRunE(leaf, nil); err != nil {
		t.Fatalf("PersistentPreRunE must not fail for a config-free command: %v", err)
	}
	if calls != 0 {
		t.Errorf("the middleware chain dereferenced f.Config %d time(s); it must stay lazy", calls)
	}
}

// testFactory builds a Factory wired with the minimum dependencies required
// to drive root.PersistentPreRunE: LogLevel + Logger (consumed by applyLogLevel
// and the structured-log context binding) and a stub Config so the
// middlewares don't blow up. No middleware may actually dereference the
// closure — see TestMiddlewareChain_DoesNotDereferenceConfig — but the stub
// keeps a factory-shape change from failing here for the wrong reason.
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

// TestRoot_CacheGroupRegistered pins the `pvmt cache prune` wiring. The
// command exists so users can reclaim the unbounded HTTP cache;
// an unregistered group would leave the eviction
// code unreachable while every unit test still passed.
func TestRoot_CacheGroupRegistered(t *testing.T) {
	cmd := NewCmdRoot(testFactory())
	sub, _, err := cmd.Find([]string{"cache", "prune"})
	if err != nil {
		t.Fatalf("`pvmt cache prune` not reachable: %v", err)
	}
	if sub.Name() != "prune" || sub.Parent().Name() != "cache" {
		t.Fatalf("resolved to %q under %q, want prune under cache", sub.Name(), sub.Parent().Name())
	}
	for _, name := range []string{"max-age", "max-size", "dry-run"} {
		if sub.Flags().Lookup(name) == nil {
			t.Errorf("`cache prune` is missing --%s", name)
		}
	}
}

// TestCompletion_GeneratesScriptForEveryShell drives `pvmt completion <shell>`
// end-to-end for every shell cobra ships and asserts each returns no error,
// emits output, and that the output contains the per-shell markers cobra is
// expected to generate. The markers guard against a cobra upgrade that
// silently changes the script shape — a regression that "non-empty" alone
// wouldn't catch.
func TestCompletion_GeneratesScriptForEveryShell(t *testing.T) {
	// Markers are substrings cobra's completion generator has emitted across
	// every release we've shipped; each is specific enough that a regression
	// producing the wrong shell's script (or no real completion logic at all)
	// would fail the check.
	shells := []struct {
		shell   string
		markers []string
	}{
		{"bash", []string{"# bash completion", "__start_pvmt", "complete "}},
		{"zsh", []string{"#compdef pvmt", "compdef _pvmt pvmt"}},
		{"fish", []string{"# fish completion for pvmt", "complete -c pvmt"}},
		{"powershell", []string{"Register-ArgumentCompleter", "'pvmt'"}},
	}
	for _, tt := range shells {
		t.Run(tt.shell, func(t *testing.T) {
			f := testFactory()
			cmd := NewCmdRoot(f)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"completion", tt.shell})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("completion %s: %v", tt.shell, err)
			}
			if out.Len() == 0 {
				t.Fatalf("completion %s emitted no output", tt.shell)
			}
			got := out.String()
			for _, m := range tt.markers {
				if !strings.Contains(got, m) {
					t.Errorf("completion %s output missing marker %q", tt.shell, m)
				}
			}
		})
	}
}

// TestCompletion_BashOutputIsValidShellSyntax pipes `pvmt completion bash`
// through `bash -n` to verify the generated script is syntactically valid
// bash — the strongest "parseable" assertion we can make without sourcing
// the script (which would mutate the shell). Skipped when bash isn't on
// PATH so the suite still runs on Windows or stripped containers.
func TestCompletion_BashOutputIsValidShellSyntax(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not on PATH: %v", err)
	}

	f := testFactory()
	cmd := NewCmdRoot(f)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"completion", "bash"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("completion bash: %v", err)
	}

	check := exec.Command(bashPath, "-n")
	check.Stdin = &out
	var stderr bytes.Buffer
	check.Stderr = &stderr
	if err := check.Run(); err != nil {
		t.Fatalf("bash -n rejected completion script: %v\nstderr: %s", err, stderr.String())
	}
}

// TestCompletion_FlagsHaveCompletions checks that root's enum-typed flags
// register a completion func, so tab-completing `pvmt --units <TAB>`
// offers metric/imperial instead of filenames. Without these registrations
// the completion script still ships but flag values aren't suggested,
// which is the user-facing half of the completion contract.
func TestCompletion_FlagsHaveCompletions(t *testing.T) {
	f := testFactory()
	cmd := NewCmdRoot(f)

	tests := []struct {
		flag string
		want []string
	}{
		{"units", []string{"metric", "imperial"}},
		{"log-level", []string{"debug", "info", "warn", "error"}},
	}
	for _, tt := range tests {
		t.Run(tt.flag, func(t *testing.T) {
			comp, ok := cmd.GetFlagCompletionFunc(tt.flag)
			if !ok {
				t.Fatalf("no completion func registered for --%s", tt.flag)
			}
			got, _ := comp(cmd, nil, "")
			if !slices.Equal(got, tt.want) {
				t.Errorf("--%s completions = %v, want %v", tt.flag, got, tt.want)
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

// TestJSONFields_SnakeCase walks the whole command tree and checks every
// --json field a command advertises is a snake_case key.
func TestJSONFields_SnakeCase(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams: ios,
		LogLevel:  new(slog.LevelVar),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: func() (*config.Config, error) {
			return &config.Config{Display: config.DisplayConfig{Units: "metric"}}, nil
		},
	}
	snake := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	annotated := 0
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if fields, ok := c.Annotations[cmdutil.JSONFieldsAnnotation]; ok {
			annotated++
			for field := range strings.SplitSeq(fields, ",") {
				if !snake.MatchString(field) || strings.HasPrefix(field, "pct_") {
					t.Errorf("%s: --json field %q is not a snake_case key (proportions are *_share fractions)", c.CommandPath(), field)
				}
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(NewCmdRoot(f))
	if annotated == 0 {
		t.Fatal("no command records its --json fields; the walk checked nothing")
	}
}

// TestJSONTagsSnakeCase parses every non-test Go file pvmt owns and checks
// each struct field's json tag name follows the JSON conventions in
// docs/architecture.md: snake_case, and no pct_ keys (proportions are 0..1
// *_share fractions). Upstream formats (internal/ingest) and the on-disk HTTP
// cache (internal/cache) keep their own names.
func TestJSONTagsSnakeCase(t *testing.T) {
	root := goldentest.RepoRoot(t)
	exempt := []string{filepath.Join("internal", "ingest"), filepath.Join("internal", "cache")}
	snake := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	checked := 0
	fset := token.NewFileSet()
	for _, top := range []string{"cmd", "internal", "pkg"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			if d.IsDir() {
				if slices.Contains(exempt, rel) || d.Name() == "testdata" || d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				field, ok := n.(*ast.Field)
				if !ok || field.Tag == nil {
					return true
				}
				tag, err := strconv.Unquote(field.Tag.Value)
				if err != nil {
					return true
				}
				name, _, _ := strings.Cut(reflect.StructTag(tag).Get("json"), ",")
				if name == "" || name == "-" {
					return true
				}
				checked++
				if !snake.MatchString(name) || strings.HasPrefix(name, "pct_") {
					t.Errorf("%s: json tag %q is not a snake_case key (proportions are *_share fractions)", fset.Position(field.Pos()), name)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if checked == 0 {
		t.Fatal("no json tags found; the walk checked nothing")
	}
}

// TestEveryRunnableCommandDeclaresArgs: a runnable
// command with no Args rule silently ignores stray positional arguments, so
// `pvmt services compute oakland` ran on the configured city, not Oakland.
// Every runnable command must say what it accepts (cobra.NoArgs for most).
// Cobra's own help and completion commands are exempt; the root stays
// non-runnable (see refuseUnknownHelpTopics).
func TestEveryRunnableCommandDeclaresArgs(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams: ios,
		LogLevel:  new(slog.LevelVar),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: func() (*config.Config, error) {
			return &config.Config{Display: config.DisplayConfig{Units: "metric"}}, nil
		},
	}
	root := NewCmdRoot(f)
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if c == root && (sub.Name() == "help" || sub.Name() == "completion") {
				continue
			}
			if sub.Runnable() && sub.Args == nil {
				t.Errorf("%q has no Args rule; stray positional arguments would be ignored", sub.CommandPath())
			}
			walk(sub)
		}
	}
	walk(root)
}
