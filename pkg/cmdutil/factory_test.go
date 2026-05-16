package cmdutil_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
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
