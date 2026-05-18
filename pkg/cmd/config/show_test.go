package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

func buildFactory(cfg *config.Config) (*cmdutil.Factory, *testBuffers) {
	ios, _, out, errOut := iostreams.Test()
	return &cmdutil.Factory{
		IOStreams: ios,
		Config:    func() (*config.Config, error) { return cfg, nil },
	}, &testBuffers{out: out, errOut: errOut}
}

type testBuffers struct {
	out    interface{ String() string }
	errOut interface{ String() string }
}

// executeShow registers the config show command on a fresh root (with the
// --units persistent flag, to mirror production) and runs it with args.
func executeShow(t *testing.T, f *cmdutil.Factory, args ...string) error {
	t.Helper()
	root := &cobra.Command{Use: "pvmt"}
	root.PersistentFlags().String("units", "", "")
	root.AddCommand(NewCmdConfig(f))
	root.SetArgs(append([]string{"config", "show"}, args...))
	root.SetOut(f.IOStreams.Out)
	root.SetErr(f.IOStreams.ErrOut)
	return root.Execute()
}

func TestNewCmdShow_RunFInjection(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	cfg := &config.Config{Cities: []config.CityConfig{{Name: "Test"}}}
	f := &cmdutil.Factory{
		IOStreams: ios,
		Config:    func() (*config.Config, error) { return cfg, nil },
	}

	var gotOpts *Options
	showCmd := NewCmdShow(f, func(opts *Options) error {
		gotOpts = opts
		return nil
	})

	root := &cobra.Command{Use: "pvmt"}
	root.PersistentFlags().String("units", "", "")
	root.AddCommand(showCmd)
	root.SetArgs([]string{"show", "--sources", "--units=metric"})
	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotOpts == nil {
		t.Fatal("runF was not invoked")
	}
	if gotOpts.IO != ios {
		t.Errorf("opts.IO not propagated from factory")
	}
	if !gotOpts.Sources {
		t.Errorf("opts.Sources should reflect --sources flag")
	}
	if gotOpts.FlagUnits != "metric" {
		t.Errorf("opts.FlagUnits = %q, want %q", gotOpts.FlagUnits, "metric")
	}
}

// TestShow_DefaultEmitsTOML locks in byob-iostreams.3 routing for a pure-
// data command: the rendered TOML is the command's DATA and must land on
// Out alone. ErrOut stays silent so `pvmt config show > pvmt.toml`
// produces a valid file with no chatter mixed in.
func TestShow_DefaultEmitsTOML(t *testing.T) {
	cfg := &config.Config{
		Grid:     config.GridConfig{HexEdgeM: 100},
		Forecast: config.ForecastConfig{Years: 20, InitialPCI: 85},
		Cities:   []config.CityConfig{{Name: "Detroit"}},
	}
	f, bufs := buildFactory(cfg)
	if err := executeShow(t, f); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := bufs.out.String()
	if !strings.Contains(got, "hex_edge_m") {
		t.Errorf("TOML output missing hex_edge_m: %q", got)
	}
	if !strings.Contains(got, "[[cities]]") {
		t.Errorf("TOML output missing [[cities]] array: %q", got)
	}
	if errs := bufs.errOut.String(); errs != "" {
		t.Errorf("stderr should be empty for pure-data command (byob-iostreams.3); got: %q", errs)
	}
}

func TestShow_SourcesEmitsKeyValueSourceLines(t *testing.T) {
	os.Unsetenv("PVMT_UNITS")
	os.Unsetenv("PVMT_HEX_EDGE_M")
	os.Unsetenv("PVMT_FORECAST_YEARS")
	os.Unsetenv("PVMT_FORECAST_INITIAL_PCI")
	t.Cleanup(func() {
		os.Unsetenv("PVMT_UNITS")
		os.Unsetenv("PVMT_HEX_EDGE_M")
		os.Unsetenv("PVMT_FORECAST_YEARS")
		os.Unsetenv("PVMT_FORECAST_INITIAL_PCI")
	})

	t.Setenv("PVMT_UNITS", "metric")
	cfg := &config.Config{
		Display:  config.DisplayConfig{Units: "imperial"},
		Grid:     config.GridConfig{HexEdgeM: 100},
		Forecast: config.ForecastConfig{Years: 25},
		Cities:   []config.CityConfig{{Name: "Detroit", HexEdgeM: 75}},
	}
	f, bufs := buildFactory(cfg)
	if err := executeShow(t, f, "--sources"); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := bufs.out.String()

	wants := []string{
		"units = metric (env:PVMT_UNITS)",
		"grid.hex_edge_m = 100 (file:grid.hex_edge_m)",
		"forecast.years = 25 (file:forecast.years)",
		"forecast.initial_pci = 85 (default)",
		"cities[detroit].hex_edge_m = 75 (file:cities[detroit].hex_edge_m)",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("--sources output missing %q\nfull output:\n%s", w, got)
		}
	}
}

func TestShow_FlagUnitsReportedAsSource(t *testing.T) {
	t.Setenv("PVMT_UNITS", "metric") // env should lose to flag
	cfg := &config.Config{Cities: []config.CityConfig{{Name: "Test"}}}
	f, bufs := buildFactory(cfg)
	if err := executeShow(t, f, "--units=imperial", "--sources"); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := bufs.out.String()
	if !strings.Contains(got, "units = imperial (flag:--units)") {
		t.Errorf("--units flag should be reported as source; got:\n%s", got)
	}
}

func TestShow_JSONEmitsArray(t *testing.T) {
	os.Unsetenv("PVMT_UNITS")
	os.Unsetenv("PVMT_HEX_EDGE_M")
	os.Unsetenv("PVMT_FORECAST_YEARS")
	os.Unsetenv("PVMT_FORECAST_INITIAL_PCI")
	t.Cleanup(func() {
		os.Unsetenv("PVMT_UNITS")
		os.Unsetenv("PVMT_HEX_EDGE_M")
		os.Unsetenv("PVMT_FORECAST_YEARS")
		os.Unsetenv("PVMT_FORECAST_INITIAL_PCI")
	})

	cfg := &config.Config{
		Grid:   config.GridConfig{HexEdgeM: 100},
		Cities: []config.CityConfig{{Name: "Test"}},
	}
	f, bufs := buildFactory(cfg)
	if err := executeShow(t, f, "--json", "key,value,source"); err != nil {
		t.Fatalf("execute: %v", err)
	}

	var rows []map[string]any
	if err := json.Unmarshal([]byte(bufs.out.String()), &rows); err != nil {
		t.Fatalf("unmarshal JSON: %v\noutput: %s", err, bufs.out.String())
	}
	if len(rows) == 0 {
		t.Fatal("expected at least one row")
	}

	var unitsRow map[string]any
	for _, r := range rows {
		if r["key"] == "units" {
			unitsRow = r
			break
		}
	}
	if unitsRow == nil {
		t.Fatal("units row missing from JSON output")
	}
	src, ok := unitsRow["source"].(map[string]any)
	if !ok {
		t.Fatalf("source is not an object: %v", unitsRow["source"])
	}
	if src["kind"] != "default" {
		t.Errorf("units source kind = %v, want default", src["kind"])
	}
}

func TestShow_SourcesAndJSONMutuallyExclusive(t *testing.T) {
	cfg := &config.Config{Cities: []config.CityConfig{{Name: "Test"}}}
	f, _ := buildFactory(cfg)
	err := executeShow(t, f, "--sources", "--json", "key")
	if err == nil {
		t.Fatal("expected --sources and --json to be mutually exclusive")
	}
	if !strings.Contains(err.Error(), "sources") || !strings.Contains(err.Error(), "json") {
		t.Errorf("error should mention both flag names: %v", err)
	}
}

func TestShow_InvalidJSONFieldRejected(t *testing.T) {
	cfg := &config.Config{Cities: []config.CityConfig{{Name: "Test"}}}
	f, _ := buildFactory(cfg)
	err := executeShow(t, f, "--json", "bogus")
	if err == nil {
		t.Fatal("expected unknown field to error")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error should name the invalid field: %v", err)
	}
}
