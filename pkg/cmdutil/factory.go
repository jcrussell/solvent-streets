package cmdutil

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"pvmt/internal/config"
	"pvmt/internal/db"
	"pvmt/internal/units"
	"pvmt/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Factory struct {
	AppVersion     string
	ExecutableName string
	IOStreams      *iostreams.IOStreams
	HttpClient     func() (*http.Client, error)
	RootDB         func() (*db.RootStore, error)
	Config         func() (*config.Config, error)
	CurrentCity    func() (*config.CityConfig, error)
	CityDB         func() (db.Store, error)
	CityFlagSet    func() bool
	UnitSystem     func() units.System
}

// AddCityOverride registers a --city/-c flag on the command and wraps
// f.CurrentCity so the flag takes precedence when set. This is called once
// during command construction (not per-request), so the mutation is safe.
func AddCityOverride(cmd *cobra.Command, f *Factory) {
	cmd.PersistentFlags().StringP("city", "c", "", "Target city name or slug")
	_ = cmd.RegisterFlagCompletionFunc("city", CitySlugCompletion(f))
	f.CurrentCity = cityOverrideFunc(cmd, f, f.CurrentCity)
	f.CityFlagSet = func() bool {
		fl := cmd.PersistentFlags().Lookup("city")
		return fl != nil && fl.Changed
	}
}

func cityOverrideFunc(cmd *cobra.Command, f *Factory, fallback func() (*config.CityConfig, error)) func() (*config.CityConfig, error) {
	return func() (*config.CityConfig, error) {
		flag := cmd.PersistentFlags().Lookup("city")
		if flag == nil || flag.Value.String() == "" {
			return fallback()
		}
		val := flag.Value.String()
		cfg, err := f.Config()
		if err != nil {
			return nil, err
		}
		slug := config.Slugify(val)
		for i := range cfg.Cities {
			if cfg.Cities[i].Slug() == slug || strings.EqualFold(cfg.Cities[i].Name, val) {
				return &cfg.Cities[i], nil
			}
		}
		return nil, fmt.Errorf("city %q not found in config", val)
	}
}

// ForEachCity resolves cities from config (all if no --city flag, one if set)
// and calls fn for each. Prints a city header when iterating multiple cities.
// Collects and joins all errors; ErrNoResults is silently skipped.
func ForEachCity(ctx context.Context, f *Factory, fn func(cf *Factory, city *config.CityConfig) error) error {
	cities, err := ResolveCities(f)
	if err != nil {
		return err
	}

	if len(cities) == 1 {
		return fn(f, &cities[0])
	}

	var errs []error
	for _, city := range cities {
		fmt.Fprintf(f.IOStreams.ErrOut, "\n=== %s ===\n", city.Name)
		if err := fn(withCity(ctx, f, &city), &city); err != nil {
			if errors.Is(err, ErrNoResults) {
				continue
			}
			errs = append(errs, fmt.Errorf("%s: %w", city.Name, err))
		}
	}
	return errors.Join(errs...)
}

// ResolveCities returns the single city selected by --city, or all configured
// cities when no flag is set. Callers that need the filtered list without the
// ForEachCity loop (e.g. export, serve) use this directly.
func ResolveCities(f *Factory) ([]config.CityConfig, error) {
	if f.CityFlagSet != nil && f.CityFlagSet() {
		city, err := f.CurrentCity()
		if err != nil {
			return nil, err
		}
		return []config.CityConfig{*city}, nil
	}
	cfg, err := f.Config()
	if err != nil {
		return nil, err
	}
	if len(cfg.Cities) == 0 {
		return nil, errors.New("no cities configured")
	}
	return cfg.Cities, nil
}

func withCity(ctx context.Context, f *Factory, city *config.CityConfig) *Factory {
	cp := *f
	c := *city
	cp.CurrentCity = func() (*config.CityConfig, error) { return &c, nil }
	cp.CityFlagSet = func() bool { return true }
	cp.CityDB = func() (db.Store, error) {
		root, err := f.RootDB()
		if err != nil {
			return nil, err
		}
		id, err := root.EnsureCity(ctx, c.Slug(), c.Name)
		if err != nil {
			return nil, err
		}
		return root.ForCity(id), nil
	}
	return &cp
}
