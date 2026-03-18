package ingest

import (
	"fmt"
	"net/http"

	"pvmt/internal/config"
	"pvmt/internal/db"
	ingestpkg "pvmt/internal/ingest"
	"pvmt/internal/resource"
	"pvmt/pkg/cmdutil"
	"pvmt/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Options struct {
	IO           *iostreams.IOStreams
	DB           func() (db.Store, error)
	Config       func() (*config.Config, error)
	HttpClient   func() (*http.Client, error)
	ResourceType resource.ResourceType
	Source       string
	Force        bool
}

func NewCmdIngest(f *cmdutil.Factory, rt resource.ResourceType, runF func(*Options) error) *cobra.Command {
	opts := &Options{
		IO:           f.IOStreams,
		DB:           f.DB,
		Config:       f.Config,
		HttpClient:   f.HttpClient,
		ResourceType: rt,
	}

	cmd := &cobra.Command{
		Use:   "ingest",
		Short: fmt.Sprintf("Ingest %s data from APIs", rt.Name()),
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(opts)
			}
			return runIngest(opts)
		},
	}

	cmd.Flags().StringVar(&opts.Source, "source", "all", "Data source (overpass|arcgis|all)")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Bypass HTTP cache")

	return cmd
}

func runIngest(opts *Options) error {
	ios := opts.IO

	cfg, err := opts.Config()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	client, err := opts.HttpClient()
	if err != nil {
		return fmt.Errorf("http client: %w", err)
	}

	store, err := opts.DB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	bbox := cfg.Area.BBox
	arcgisURL := cfg.Sources.ArcGISURL

	var sources []ingestpkg.Source
	if opts.Source == "all" {
		sources = ingestpkg.AllSources(bbox, arcgisURL)
	} else {
		src, err := ingestpkg.SourceByName(opts.Source, bbox, arcgisURL)
		if err != nil {
			return &cmdutil.FlagError{Err: err}
		}
		sources = []ingestpkg.Source{src}
	}

	var allFeatures []db.Feature
	for _, src := range sources {
		fmt.Fprintf(ios.Out, "Fetching %s data from %s...\n", opts.ResourceType.Name(), src.Name())
		features, err := src.Fetch(client, opts.ResourceType)
		if err != nil {
			fmt.Fprintf(ios.ErrOut, "Warning: %s fetch failed: %v\n", src.Name(), err)
			continue
		}
		fmt.Fprintf(ios.Out, "  Got %d features from %s\n", len(features), src.Name())
		allFeatures = append(allFeatures, features...)
	}

	if len(allFeatures) == 0 {
		fmt.Fprintf(ios.ErrOut, "No features fetched for %s\n", opts.ResourceType.Name())
		return cmdutil.ErrNoResults
	}

	fmt.Fprintf(ios.Out, "Saving %d features to database...\n", len(allFeatures))
	if err := store.UpsertFeatures(opts.ResourceType.Name(), allFeatures); err != nil {
		return fmt.Errorf("save features: %w", err)
	}

	fmt.Fprintf(ios.Out, "Done. Ingested %d %s features.\n", len(allFeatures), opts.ResourceType.Name())
	return nil
}
