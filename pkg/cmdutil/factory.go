package cmdutil

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/paths"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmd/prompt"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Factory struct {
	AppVersion     string
	ExecutableName string
	IOStreams      *iostreams.IOStreams
	HttpClient     func() (*http.Client, error)
	RootDB         func() (*db.RootStore, error)
	// CloseRootDB releases the RootDB handle at process shutdown, but only
	// if RootDB was actually opened during the run — commands that never
	// touch the database (--version, --help) pay nothing and never create
	// the DB file just to close it. Set by factory.New; Main defers it.
	// Nil-safe: a nil closure means "nothing to close".
	CloseRootDB func() error
	Config      func() (*config.Config, error)
	CurrentCity func() (*config.CityConfig, error)
	CityDB      func() (db.Store, error)
	CityFlagSet func() bool
	UnitSystem  func() units.System
	Paths       func() (*paths.Paths, error)

	// Prompter is the interactive-input surface for destructive or
	// otherwise-prompting commands (byob-prompter.1). Eager like
	// IOStreams: construction is cheap, and a per-call closure adds
	// nothing here. Tests replace it with prompt.Stub; production uses
	// prompt.NewLive(IOStreams). Commands that prompt should also
	// accept a --yes flag (byob-prompter.5) and short-circuit before
	// calling Prompter when the flag is set.
	Prompter prompt.Prompter

	// Logger is the base slog.Logger wired to IOStreams.ErrOut. Per
	// byob-logging.2 it is eager (cheap to construct) rather than lazy.
	// The root command's PersistentPreRunE clones it with per-run
	// attributes and attaches the result to cmd.Context() via
	// internal/logs.WithLogger; command bodies pull it back out via
	// logs.From(ctx) or use slog.InfoContext/slog.Default.
	Logger *slog.Logger

	// LogLevel is the handler's level knob. PersistentPreRunE sets it
	// from --log-level / -v / PVMT_LOG so existing Logger instances
	// (snapshotted into Options) see the updated level.
	LogLevel *slog.LevelVar

	// requestCtx is the per-invocation context installed by the root
	// command's PersistentPreRunE (SetRequestContext). Lazily-built
	// closures such as CityDB — constructed in factory.New() with no
	// request ctx — read it via RequestContext() so EnsureCity honors
	// cancellation instead of falling back to context.Background().
	// It is an interface value, so copying the Factory by value is safe
	// (no copylocks). See finding 0nyu.
	requestCtx context.Context

	// cityStores memoizes city-scoped stores so repeated CityDB() calls
	// (and value-copies made by withCity) don't re-run EnsureCity on
	// every access. It lives behind a pointer so a copied Factory shares
	// the same cache; it is keyed by (slug, configID) so a copy scoped to
	// city A never hands back city B's store. See finding xygq.
	cityStores *storeCache
}

// SetRequestContext installs the per-invocation context (called from the
// root command's PersistentPreRunE). Later closures read it via
// RequestContext.
func (f *Factory) SetRequestContext(ctx context.Context) { f.requestCtx = ctx }

// RequestContext returns the per-invocation context set by
// SetRequestContext, or context.Background() when none was installed (e.g.
// commands exempt from PersistentPreRunE, or a test factory built directly).
func (f *Factory) RequestContext() context.Context {
	if f.requestCtx != nil {
		return f.requestCtx
	}
	return context.Background()
}

// storeCache memoizes db.Store values by an opaque key. It is shared across
// value-copies of a Factory via a pointer, so the embedded Mutex is never
// copied (no copylocks).
type storeCache struct {
	mu     sync.Mutex
	stores map[string]db.Store
}

func newStoreCache() *storeCache {
	return &storeCache{stores: make(map[string]db.Store)}
}

// get returns the cached store for key, building and caching it on a miss.
// Errors are not cached so a transient failure can be retried on the next
// call. The build runs under the lock so two concurrent callers for the same
// key don't both run EnsureCity.
func (c *storeCache) get(key string, build func() (db.Store, error)) (db.Store, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.stores[key]; ok {
		return s, nil
	}
	s, err := build()
	if err != nil {
		return nil, err
	}
	c.stores[key] = s
	return s, nil
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
		var matches []int
		for i := range cfg.Cities {
			if cfg.Cities[i].Slug() == slug || strings.EqualFold(cfg.Cities[i].Name, val) {
				matches = append(matches, i)
			}
		}
		switch len(matches) {
		case 0:
			return nil, fmt.Errorf("city %q not found in config", val)
		case 1:
			return &cfg.Cities[matches[0]], nil
		default:
			// Two or more [[cities]] resolve to the same slug/name (e.g. two
			// "Austin" entries): picking the first would silently operate on an
			// arbitrary one. Force the user to disambiguate their config instead.
			names := make([]string, 0, len(matches))
			for _, i := range matches {
				names = append(names, cfg.Cities[i].Name)
			}
			return nil, fmt.Errorf("city %q is ambiguous: matches %d configured cities (%s); disambiguate in pvmt.toml",
				val, len(matches), strings.Join(names, ", "))
		}
	}
}

// ForEachCity resolves cities from config (all if no --city flag, one if set)
// and calls fn for each. Prints a city header when iterating multiple cities.
// Collects and joins all errors. A single per-city ErrNoResults is skipped so
// one empty city doesn't fail a run that produced results elsewhere — but when
// EVERY city yields ErrNoResults, ForEachCity returns ErrNoResults so the
// "nothing produced" outcome maps to the same exit code (3) as the single-city
// path, instead of silently exiting 0 (solvent-streets-9zc2).
func ForEachCity(ctx context.Context, f *Factory, fn func(cf *Factory, city *config.CityConfig) error) error {
	cities, err := ResolveCities(f)
	if err != nil {
		return err
	}

	// Honor cancellation (Ctrl-C / SIGTERM) before any per-city work, in both
	// the single-city and multi-city paths.
	if err := ctx.Err(); err != nil {
		return err
	}

	if len(cities) == 1 {
		return fn(f, &cities[0])
	}

	var errs []error
	sawResult := false    // at least one city returned nil (produced output)
	skippedEmpty := false // at least one city returned ErrNoResults
	for _, city := range cities {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		fmt.Fprintf(f.IOStreams.ErrOut, "\n=== %s ===\n", city.Name)
		switch err := fn(withCity(ctx, f, &city), &city); {
		case err == nil:
			sawResult = true
		case errors.Is(err, ErrNoResults):
			skippedEmpty = true
		default:
			errs = append(errs, fmt.Errorf("%s: %w", city.Name, err))
		}
	}
	// Only when there were no real errors and no city produced anything: every
	// processed city was empty, so surface ErrNoResults (exit 3).
	if len(errs) == 0 && !sawResult && skippedEmpty {
		return ErrNoResults
	}
	return errors.Join(errs...)
}

// ResolveConfigID returns cfg.ConfigID via the given resolver, or ""
// if the resolver is nil, errors, or returns a nil config. Used as the
// EnsureCity config_id argument across commands that scope city ids
// to a specific pvmt.toml. An empty return is intentional and surfaces
// at the EnsureCity boundary as "config_id is required" — a clear
// failure mode beats silently writing a row keyed on "".
func ResolveConfigID(resolveConfig func() (*config.Config, error)) string {
	if resolveConfig == nil {
		return ""
	}
	cfg, err := resolveConfig()
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.ConfigID
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

// EnsureCityStore ensures the city row exists under configID and returns
// its city-scoped store. It deliberately does NOT apply a config-hash pin:
// snapshots commands span config hashes (rm searches all cities by id, ls
// displays the ConfigHash column, prune lists all snapshots), so pinning
// here would hide rows they need to see. Single-city pinned paths
// (buildCityDB, withCity) apply WithConfigHash themselves instead.
func EnsureCityStore(ctx context.Context, root db.RootStorer, city config.CityConfig, configID string) (db.Store, error) {
	id, err := root.EnsureCity(ctx, city.Slug(), city.Name, configID)
	if err != nil {
		return nil, fmt.Errorf("ensure city %s: %w", city.Slug(), err)
	}
	return root.ForCity(id), nil
}

func withCity(ctx context.Context, f *Factory, city *config.CityConfig) *Factory {
	cp := *f
	c := *city
	cp.CurrentCity = func() (*config.CityConfig, error) { return &c, nil }
	cp.CityFlagSet = func() bool { return true }

	// Share one store cache across the parent and all its withCity copies so
	// repeated CityDB() calls memoize EnsureCity. Lazily install it on the
	// parent (f is a pointer, and ForEachCity iterates sequentially) so a
	// bare Factory built in tests memoizes too. Keyed by (slug, configID),
	// so this per-city copy can never return a different city's store.
	if f.cityStores == nil {
		f.cityStores = newStoreCache()
	}
	cache := f.cityStores
	cp.cityStores = cache

	cp.CityDB = func() (db.Store, error) {
		root, err := f.RootDB()
		if err != nil {
			return nil, err
		}
		configID := ResolveConfigID(f.Config)
		key := c.Slug() + "\x00" + configID
		return cache.get(key, func() (db.Store, error) {
			id, err := root.EnsureCity(ctx, c.Slug(), c.Name, configID)
			if err != nil {
				return nil, err
			}
			store := root.ForCity(id)
			if cfg, cfgErr := f.Config(); cfgErr == nil && cfg != nil {
				store = store.WithConfigHash(cfg.Hash())
			}
			return store, nil
		})
	}
	return &cp
}
