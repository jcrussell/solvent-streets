package export

import (
	"context"
	"fmt"
	"os"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	exportpkg "github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/internal/logs"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

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
			return runExport(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVarP(&opts.OutputDir, "output", "o", "dist", "Output directory")
	cmd.Flags().BoolVar(&opts.Clean, "clean", false, "Remove output directory before exporting")

	return cmd
}

// Validate runs at the Options boundary per byob-input-validation.5:
// resolve the user-supplied path, reject sensitive locations, probe
// writability — all before the multi-minute export work begins. Errors
// are returned as FlagError so the runner maps them to exit code 2.
func (opts *Options) Validate() error {
	resolved, err := cmdutil.ResolveOutputDir(opts.OutputDir)
	if err != nil {
		return cmdutil.FlagErrorf("%s", err)
	}
	opts.OutputDir = resolved

	if info, err := os.Stat(opts.OutputDir); err == nil && info.IsDir() && !opts.Clean {
		return cmdutil.FlagErrorf("output directory %q already exists; use --clean to remove it first", opts.OutputDir)
	}
	if err := cmdutil.WritableDir(opts.OutputDir); err != nil {
		return cmdutil.FlagErrorf("%s", err)
	}
	return nil
}

func runExport(ctx context.Context, opts *Options) error {
	ios := opts.IO

	if err := opts.Validate(); err != nil {
		return err
	}

	if info, err := os.Stat(opts.OutputDir); err == nil && info.IsDir() {
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

	entries, err := exportpkg.BuildCityEntries(ctx, rootDB, cfg, cities)
	if err != nil {
		return err
	}

	logs.From(ctx).Info("exporting static site", "output_dir", opts.OutputDir)

	exporter := exportpkg.New(entries, cfg, opts.OutputDir, cfg.UnitSystem().String())
	if err := exporter.Run(ctx); err != nil {
		return fmt.Errorf("export: %w", err)
	}

	logs.From(ctx).Info("export complete", "output_dir", opts.OutputDir)
	// The "serve locally" hint is a user-facing tip, not a log line --
	// keep it on stdout regardless of --log-level.
	fmt.Fprintf(ios.Out, "Serve locally: cd %s && python3 -m http.server\n", opts.OutputDir)
	return nil
}
