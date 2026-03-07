package export

import (
	"fmt"

	"pvmt/internal/export"
	"pvmt/pkg/cmdutil"

	"github.com/spf13/cobra"
)

type Options struct {
	Factory  *cmdutil.Factory
	OutputDir string
}

func NewCmdExport(f *cmdutil.Factory) *cobra.Command {
	opts := &Options{
		Factory: f,
	}

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export a static HTML site with map and stats",
		Long:  "Generate a self-contained HTML site with MapLibre dashboard, hex heatmap, and summary stats.\nDeploy to GitHub Pages or any static host.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExport(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.OutputDir, "output", "o", "dist", "Output directory")

	return cmd
}

func runExport(opts *Options) error {
	ios := opts.Factory.IOStreams

	cfg, err := opts.Factory.Config()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	store, err := opts.Factory.DB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	fmt.Fprintf(ios.Out, "Exporting static site to %s/...\n", opts.OutputDir)

	exporter := export.New(store, cfg, opts.OutputDir)
	if err := exporter.Run(); err != nil {
		return fmt.Errorf("export: %w", err)
	}

	fmt.Fprintf(ios.Out, "Done. Static site exported to %s/\n", opts.OutputDir)
	fmt.Fprintf(ios.Out, "Serve locally: cd %s && python3 -m http.server\n", opts.OutputDir)
	return nil
}
