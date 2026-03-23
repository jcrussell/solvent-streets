package cmdutil

import (
	"fmt"
	"net/http"
	"strings"

	"pvmt/internal/config"
	"pvmt/internal/db"
	"pvmt/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Factory struct {
	AppVersion     string
	ExecutableName string
	IOStreams       *iostreams.IOStreams
	HttpClient     func() (*http.Client, error)
	RootDB         func() (*db.RootStore, error)
	Config         func() (*config.Config, error)
	CurrentCity    func() (*config.CityConfig, error)
	CityDB         func() (db.Store, error)
}

// AddCityOverride registers a --city/-c flag on the command and wraps
// f.CurrentCity so the flag takes precedence when set. This is called once
// during command construction (not per-request), so the mutation is safe.
func AddCityOverride(cmd *cobra.Command, f *Factory) {
	cmd.PersistentFlags().StringP("city", "c", "", "Target city name or slug")
	f.CurrentCity = cityOverrideFunc(cmd, f, f.CurrentCity)
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
