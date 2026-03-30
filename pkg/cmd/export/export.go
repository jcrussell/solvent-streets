package export

import (
	"fmt"
	"os"

	"pvmt/internal/config"
	"pvmt/internal/db"
	exportpkg "pvmt/internal/export"
	"pvmt/pkg/cmdutil"
	"pvmt/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Options struct {
	IO        *iostreams.IOStreams
	RootDB    func() (*db.RootStore, error)
	Config    func() (*config.Config, error)
	Cities    func() ([]config.CityConfig, error)
	OutputDir string
	Clean     bool
}

func NewCmdExport(f *cmdutil.Factory, runF func(*Options) error) *cobra.Command {
	opts := &Options{
		IO:     f.IOStreams,
		RootDB: f.RootDB,
		Config: f.Config,
		Cities: func() ([]config.CityConfig, error) { return cmdutil.ResolveCities(f) },
	}

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export a static HTML site with map and stats",
		Long:  "Generate a self-contained HTML site with MapLibre dashboard, hex heatmap, and summary stats.\nDeploy to GitHub Pages or any static host.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(opts)
			}
			return runExport(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.OutputDir, "output", "o", "dist", "Output directory")
	cmd.Flags().BoolVar(&opts.Clean, "clean", false, "Remove output directory before exporting")

	return cmd
}

func runExport(opts *Options) error {
	ios := opts.IO

	if info, err := os.Stat(opts.OutputDir); err == nil && info.IsDir() {
		if !opts.Clean {
			return fmt.Errorf("output directory %q already exists; use --clean to remove it first", opts.OutputDir)
		}
		if err := os.RemoveAll(opts.OutputDir); err != nil {
			return fmt.Errorf("clean output directory: %w", err)
		}
	}

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

	entries, err := exportpkg.BuildCityEntries(rootDB, cfg, cities)
	if err != nil {
		return err
	}

	fmt.Fprintf(ios.Out, "Exporting static site to %s/...\n", opts.OutputDir)

	exporter := exportpkg.New(entries, cfg, opts.OutputDir, cfg.UnitSystem().String())
	if err := exporter.Run(); err != nil {
		return fmt.Errorf("export: %w", err)
	}

	fmt.Fprintf(ios.Out, "Done. Static site exported to %s/\n", opts.OutputDir)
	fmt.Fprintf(ios.Out, "Serve locally: cd %s && python3 -m http.server\n", opts.OutputDir)
	return nil
}
