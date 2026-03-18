package export

import (
	"fmt"

	"pvmt/internal/config"
	"pvmt/internal/db"
	exportpkg "pvmt/internal/export"
	"pvmt/pkg/cmdutil"
	"pvmt/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Options struct {
	IO        *iostreams.IOStreams
	DB        func() (db.Store, error)
	Config    func() (*config.Config, error)
	OutputDir string
}

func NewCmdExport(f *cmdutil.Factory, runF func(*Options) error) *cobra.Command {
	opts := &Options{
		IO:     f.IOStreams,
		DB:     f.DB,
		Config: f.Config,
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

	return cmd
}

func runExport(opts *Options) error {
	ios := opts.IO

	cfg, err := opts.Config()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	store, err := opts.DB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	fmt.Fprintf(ios.Out, "Exporting static site to %s/...\n", opts.OutputDir)

	exporter := exportpkg.New(store, cfg, opts.OutputDir)
	if err := exporter.Run(); err != nil {
		return fmt.Errorf("export: %w", err)
	}

	fmt.Fprintf(ios.Out, "Done. Static site exported to %s/\n", opts.OutputDir)
	fmt.Fprintf(ios.Out, "Serve locally: cd %s && python3 -m http.server\n", opts.OutputDir)
	return nil
}
