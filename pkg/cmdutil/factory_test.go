package cmdutil_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

// TestForEachCity pins the multi-city iteration contract used by every
// `pvmt all *` subcommand:
//
//   - Single configured city: callback runs once, no city-header chatter on
//     ErrOut (so a single-city user doesn't see the multi-city framing).
//   - Multiple cities: each gets a header on ErrOut and the callback runs
//     for every one, even if an earlier callback errored.
//   - cmdutil.ErrNoResults from the callback: silently skipped — that city
//     simply had nothing to report, not a failure.
//   - Any other error: aggregated via errors.Join and surfaced, so the
//     caller sees the full picture and `errors.Is` reaches each underlying
//     error.
//   - CityFlagSet → ResolveCities returns only the selected city and
//     ForEachCity drives the callback once.
//
// Today this contract is enforced only by hand inspection (pkg/cmd/all has
// 0% coverage). A regression — say, returning early on the first error or
// stopping at ErrNoResults — would silently change CLI behavior for every
// multi-city user.
func TestForEachCity(t *testing.T) {
	cityA := config.CityConfig{Name: "Alpha"}
	cityB := config.CityConfig{Name: "Beta"}
	cityC := config.CityConfig{Name: "Gamma"}

	newFactory := func(cities []config.CityConfig, flagSet bool, current *config.CityConfig) (*cmdutil.Factory, func() string) {
		ios, _, _, errOut := iostreams.Test()
		cfg := &config.Config{Cities: cities}
		f := &cmdutil.Factory{
			IOStreams:   ios,
			Config:      func() (*config.Config, error) { return cfg, nil },
			CityFlagSet: func() bool { return flagSet },
			CurrentCity: func() (*config.CityConfig, error) { return current, nil },
		}
		return f, errOut.String
	}

	t.Run("single_city_no_header", func(t *testing.T) {
		f, errOut := newFactory([]config.CityConfig{cityA}, false, nil)

		var calls []string
		err := cmdutil.ForEachCity(context.Background(), f, func(_ *cmdutil.Factory, city *config.CityConfig) error {
			calls = append(calls, city.Name)
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(calls) != 1 || calls[0] != "Alpha" {
			t.Errorf("calls = %v; want [Alpha]", calls)
		}
		if got := errOut(); got != "" {
			t.Errorf("single-city errOut = %q; want empty (no header)", got)
		}
	})

	t.Run("multi_city_each_gets_header", func(t *testing.T) {
		f, errOut := newFactory([]config.CityConfig{cityA, cityB, cityC}, false, nil)

		var calls []string
		err := cmdutil.ForEachCity(context.Background(), f, func(_ *cmdutil.Factory, city *config.CityConfig) error {
			calls = append(calls, city.Name)
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(calls) != 3 {
			t.Errorf("calls = %v; want all three cities visited", calls)
		}
		out := errOut()
		for _, name := range []string{"Alpha", "Beta", "Gamma"} {
			if !strings.Contains(out, "=== "+name+" ===") {
				t.Errorf("missing header for %q in:\n%s", name, out)
			}
		}
	})

	t.Run("err_no_results_silently_skipped", func(t *testing.T) {
		f, _ := newFactory([]config.CityConfig{cityA, cityB}, false, nil)

		err := cmdutil.ForEachCity(context.Background(), f, func(_ *cmdutil.Factory, city *config.CityConfig) error {
			if city.Name == "Alpha" {
				return cmdutil.ErrNoResults
			}
			return nil
		})
		if err != nil {
			t.Errorf("ErrNoResults must be silently skipped; got %v", err)
		}
	})

	t.Run("all_cities_empty_returns_err_no_results", func(t *testing.T) {
		// When EVERY city yields ErrNoResults, the multi-city path must return
		// ErrNoResults (exit 3), matching the single-city path — not silently
		// exit 0 (solvent-streets-9zc2).
		f, _ := newFactory([]config.CityConfig{cityA, cityB}, false, nil)

		err := cmdutil.ForEachCity(context.Background(), f, func(_ *cmdutil.Factory, _ *config.CityConfig) error {
			return cmdutil.ErrNoResults
		})
		if !errors.Is(err, cmdutil.ErrNoResults) {
			t.Errorf("all-empty multi-city: want ErrNoResults, got %v", err)
		}
	})

	t.Run("single_city_err_no_results_propagates", func(t *testing.T) {
		// The single-city contract ForEachCity's all-empty case now mirrors.
		f, _ := newFactory([]config.CityConfig{cityA}, false, nil)

		err := cmdutil.ForEachCity(context.Background(), f, func(_ *cmdutil.Factory, _ *config.CityConfig) error {
			return cmdutil.ErrNoResults
		})
		if !errors.Is(err, cmdutil.ErrNoResults) {
			t.Errorf("single-city ErrNoResults must propagate, got %v", err)
		}
	})

	t.Run("real_error_joined_other_cities_still_run", func(t *testing.T) {
		f, _ := newFactory([]config.CityConfig{cityA, cityB, cityC}, false, nil)
		sentinel := errors.New("boom")

		var calls []string
		err := cmdutil.ForEachCity(context.Background(), f, func(_ *cmdutil.Factory, city *config.CityConfig) error {
			calls = append(calls, city.Name)
			if city.Name == "Alpha" {
				return sentinel
			}
			return nil
		})
		if err == nil {
			t.Fatal("expected joined error, got nil")
		}
		if !errors.Is(err, sentinel) {
			t.Errorf("errors.Is(err, sentinel) = false; err = %v", err)
		}
		if len(calls) != 3 {
			t.Errorf("real error must not short-circuit; calls = %v", calls)
		}
	})

	t.Run("cancelled_context_stops_before_any_city", func(t *testing.T) {
		f, _ := newFactory([]config.CityConfig{cityA, cityB, cityC}, false, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		var calls []string
		err := cmdutil.ForEachCity(ctx, f, func(_ *cmdutil.Factory, city *config.CityConfig) error {
			calls = append(calls, city.Name)
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("want context.Canceled, got %v", err)
		}
		if len(calls) != 0 {
			t.Errorf("cancelled context must run no cities; calls = %v", calls)
		}
	})

	t.Run("cancelled_context_honored_single_city", func(t *testing.T) {
		f, _ := newFactory([]config.CityConfig{cityA}, false, nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		called := false
		err := cmdutil.ForEachCity(ctx, f, func(_ *cmdutil.Factory, _ *config.CityConfig) error {
			called = true
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("want context.Canceled, got %v", err)
		}
		if called {
			t.Error("cancelled context must not invoke fn in single-city path")
		}
	})

	t.Run("city_flag_runs_only_selected_city", func(t *testing.T) {
		f, _ := newFactory([]config.CityConfig{cityA, cityB, cityC}, true, &cityB)

		var calls []string
		err := cmdutil.ForEachCity(context.Background(), f, func(_ *cmdutil.Factory, city *config.CityConfig) error {
			calls = append(calls, city.Name)
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(calls) != 1 || calls[0] != "Beta" {
			t.Errorf("CityFlagSet should drive single-city run; calls = %v", calls)
		}
	})
}

// TestCityOverride_AmbiguousMatch pins finding 79hj: when two or more
// [[cities]] resolve to the same slug/name, `--city X` must fail with a clear
// ambiguity error rather than silently operating on the first match. A unique
// match still resolves, and an unknown value still reports "not found".
func TestCityOverride_AmbiguousMatch(t *testing.T) {
	newFactory := func(cities []config.CityConfig) (*cobra.Command, *cmdutil.Factory) {
		cfg := &config.Config{Cities: cities}
		f := &cmdutil.Factory{
			Config:      func() (*config.Config, error) { return cfg, nil },
			CurrentCity: func() (*config.CityConfig, error) { return &cities[0], nil },
		}
		cmd := &cobra.Command{Use: "pvmt"}
		cmdutil.AddCityOverride(cmd, f)
		return cmd, f
	}

	t.Run("ambiguous name errors", func(t *testing.T) {
		cmd, f := newFactory([]config.CityConfig{{Name: "Austin"}, {Name: "Austin"}})
		if err := cmd.PersistentFlags().Set("city", "Austin"); err != nil {
			t.Fatal(err)
		}
		_, err := f.CurrentCity()
		if err == nil {
			t.Fatal("expected ambiguity error, got nil")
		}
		if !strings.Contains(err.Error(), "ambiguous") {
			t.Errorf("expected ambiguity error, got %v", err)
		}
	})

	t.Run("unique match resolves", func(t *testing.T) {
		cmd, f := newFactory([]config.CityConfig{{Name: "Austin"}, {Name: "Oakland"}})
		if err := cmd.PersistentFlags().Set("city", "Oakland"); err != nil {
			t.Fatal(err)
		}
		city, err := f.CurrentCity()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if city.Name != "Oakland" {
			t.Errorf("got %q, want Oakland", city.Name)
		}
	})

	t.Run("unknown value not found", func(t *testing.T) {
		cmd, f := newFactory([]config.CityConfig{{Name: "Austin"}, {Name: "Oakland"}})
		if err := cmd.PersistentFlags().Set("city", "Nowhere"); err != nil {
			t.Fatal(err)
		}
		_, err := f.CurrentCity()
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected not-found error, got %v", err)
		}
	})
}

// TestResolveConfigID pins the contract that the helper returns
// cfg.ConfigID, not cfg.SourcePath. This is load-bearing because every
// `pvmt all|status|snapshots` subcommand uses ResolveConfigID to key
// the cities table; before solvent-streets-kevc's redesign the helper
// returned SourcePath, which leaked $HOME paths into the DB and broke
// gensite/pvmt-all collation. A regression to SourcePath would
// reintroduce the kevc symptom without breaking any other test.
func TestResolveConfigID(t *testing.T) {
	t.Run("returns ConfigID not SourcePath", func(t *testing.T) {
		cfg := &config.Config{
			ConfigID:   "expected-id",
			SourcePath: "/home/someone/secret/path.toml",
		}
		got := cmdutil.ResolveConfigID(func() (*config.Config, error) { return cfg, nil }, nil)
		if got != "expected-id" {
			t.Errorf("ResolveConfigID = %q, want %q (must be ConfigID, not SourcePath)", got, "expected-id")
		}
	})

	t.Run("nil resolver returns empty string", func(t *testing.T) {
		got := cmdutil.ResolveConfigID(nil, nil)
		if got != "" {
			t.Errorf("ResolveConfigID(nil) = %q, want empty string", got)
		}
	})

	t.Run("resolver error returns empty string", func(t *testing.T) {
		got := cmdutil.ResolveConfigID(func() (*config.Config, error) { return nil, errors.New("boom") }, nil)
		if got != "" {
			t.Errorf("ResolveConfigID on error = %q, want empty string", got)
		}
	})

	// An included city belongs to the file that DECLARED it, not the one that
	// included it — that is what lets a union config read the data its source
	// examples ingested. A nil city keeps the config's own id.
	t.Run("included city keeps its source config id", func(t *testing.T) {
		cfg := &config.Config{ConfigID: "union"}
		included := &config.CityConfig{Name: "Oakland, CA", SourceConfigID: "bay-area-ca"}
		if got := cmdutil.ResolveConfigID(func() (*config.Config, error) { return cfg, nil }, included); got != "bay-area-ca" {
			t.Errorf("ResolveConfigID = %q, want %q (the source config's id)", got, "bay-area-ca")
		}
		direct := &config.CityConfig{Name: "Bend, OR"}
		if got := cmdutil.ResolveConfigID(func() (*config.Config, error) { return cfg, nil }, direct); got != "union" {
			t.Errorf("ResolveConfigID = %q, want %q (a directly-declared city keeps the config's own id)", got, "union")
		}
	})
}
