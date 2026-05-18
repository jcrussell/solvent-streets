package export

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
	JSON      bool
}

func NewCmdExport(f *cmdutil.Factory, runF func(context.Context, *Options) error) *cobra.Command {
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
		Example: `  # Export the default static site to ./dist
  pvmt export

  # Pick an output directory and overwrite an existing one
  pvmt export --output build --clean

  # Emit a machine-readable manifest of files written (stdout)
  pvmt export --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runExport(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVarP(&opts.OutputDir, "output", "o", "dist", "Output directory")
	cmd.Flags().BoolVar(&opts.Clean, "clean", false, "Remove output directory before exporting")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "Emit a JSON manifest of written files to stdout instead of human chatter")

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

	if opts.JSON {
		manifest, err := buildManifest(opts.OutputDir, entries)
		if err != nil {
			return fmt.Errorf("manifest: %w", err)
		}
		out, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal manifest: %w", err)
		}
		fmt.Fprintln(ios.Out, string(out))
		return nil
	}

	// The "serve locally" hint is chatter, not data: route to ErrOut so
	// stdout stays empty and pipelines (`pvmt export > log`) capture only
	// the data path — none here — and never this hint (byob-iostreams.3).
	fmt.Fprintf(ios.ErrOut, "Serve locally: cd %s && python3 -m http.server\n", opts.OutputDir)
	return nil
}

// manifest is the --json shape: one summary object describing the
// export, with files grouped per city. Shared assets (index.html, WASM,
// cities.json) are reported under Shared so consumers can tell them
// apart from city-specific data.
type manifest struct {
	OutputDir string         `json:"output_dir"`
	Total     int            `json:"total"`
	Cities    []manifestCity `json:"cities"`
	Shared    []string       `json:"shared,omitempty"`
}

type manifestCity struct {
	Slug      string   `json:"slug"`
	Name      string   `json:"name"`
	FileCount int      `json:"file_count"`
	Files     []string `json:"files"`
}

// buildManifest walks outputDir and attributes each file to a city by
// path prefix. The exporter writes single-city sites with data files at
// outputDir/data/ and multi-city sites at outputDir/cities/<slug>/data/
// — buildManifest mirrors that layout to produce the per-city groupings.
// File paths are returned as forward-slash relative paths so the JSON
// shape is identical on Windows and Unix.
func buildManifest(outputDir string, entries []exportpkg.CityEntry) (manifest, error) {
	m := manifest{OutputDir: outputDir}

	bySlug := make(map[string][]string, len(entries))
	for _, e := range entries {
		bySlug[e.Slug] = nil
	}
	singleSlug := ""
	if len(entries) == 1 {
		singleSlug = entries[0].Slug
	}

	err := filepath.WalkDir(outputDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, relErr := filepath.Rel(outputDir, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		m.Total++
		if slug := attributeFile(rel, singleSlug, bySlug); slug != "" {
			bySlug[slug] = append(bySlug[slug], rel)
		} else {
			m.Shared = append(m.Shared, rel)
		}
		return nil
	})
	if err != nil {
		return m, err
	}

	for _, e := range entries {
		files := bySlug[e.Slug]
		sort.Strings(files)
		m.Cities = append(m.Cities, manifestCity{
			Slug:      e.Slug,
			Name:      e.City.Name,
			FileCount: len(files),
			Files:     files,
		})
	}
	sort.Strings(m.Shared)
	return m, nil
}

// attributeFile returns the slug a relative file path belongs to, or ""
// if it's a shared asset. singleSlug is non-empty in single-city mode;
// it's where data/* files land. In multi-city mode files under
// cities/<slug>/ attach to that slug when it matches a known entry.
func attributeFile(rel, singleSlug string, bySlug map[string][]string) string {
	if singleSlug != "" {
		if strings.HasPrefix(rel, "data/") {
			return singleSlug
		}
		return ""
	}
	if !strings.HasPrefix(rel, "cities/") {
		return ""
	}
	parts := strings.SplitN(rel, "/", 3)
	if len(parts) < 2 {
		return ""
	}
	if _, ok := bySlug[parts[1]]; !ok {
		return ""
	}
	return parts[1]
}
