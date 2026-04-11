package serve

import (
	"context"
	"fmt"

	"pvmt/internal/config"
	"pvmt/internal/db"
	exportpkg "pvmt/internal/export"
	"pvmt/internal/server"
	"pvmt/pkg/cmdutil"
	"pvmt/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Options struct {
	IO     *iostreams.IOStreams
	RootDB func() (*db.RootStore, error)
	Config func() (*config.Config, error)
	Cities func() ([]config.CityConfig, error)
	Port   int
}

func NewCmdServe(f *cmdutil.Factory, runF func(*Options) error) *cobra.Command {
	opts := &Options{
		IO:     f.IOStreams,
		RootDB: f.RootDB,
		Config: f.Config,
		Cities: func() ([]config.CityConfig, error) { return cmdutil.ResolveCities(f) },
	}

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the web server",
		Long:  "Start the MapLibre visualization server with API endpoints.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(opts)
			}
			return runServe(cmd.Context(), opts)
		},
	}

	cmd.Flags().IntVar(&opts.Port, "port", 8080, "Port to listen on")

	return cmd
}

func runServe(ctx context.Context, opts *Options) error {
	cfg, err := opts.Config()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	rootDB, err := opts.RootDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	cities, err := opts.Cities()
	if err != nil {
		return fmt.Errorf("cities: %w", err)
	}

	entries, err := exportpkg.BuildCityEntries(ctx, rootDB, cfg, cities)
	if err != nil {
		return err
	}

	srv := server.New(entries, opts.Port)
	return srv.ListenAndServe()
}
