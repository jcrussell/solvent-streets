package ingest

import (
	"context"
	"fmt"
	"net/http"

	"pvmt/internal/config"
	"pvmt/internal/db"
	"pvmt/internal/geo"
	ingestpkg "pvmt/internal/ingest"
	"pvmt/internal/resource"
	"pvmt/pkg/cmdutil"
	"pvmt/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Options struct {
	IO           *iostreams.IOStreams
	CityDB       func() (db.Store, error)
	CurrentCity  func() (*config.CityConfig, error)
	HttpClient   func() (*http.Client, error)
	ResourceType resource.ResourceType
	Source       string
	Force        bool
}

func NewCmdIngest(f *cmdutil.Factory, rt resource.ResourceType, runF func(*Options) error) *cobra.Command {
	opts := &Options{
		IO:           f.IOStreams,
		CityDB:       f.CityDB,
		CurrentCity:  f.CurrentCity,
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
			return runIngestAllCities(cmd.Context(), f, opts)
		},
	}

	cmd.Flags().StringVar(&opts.Source, "source", "all", "Data source (overpass|arcgis|all)")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Bypass HTTP cache")

	return cmd
}

func runIngestAllCities(ctx context.Context, f *cmdutil.Factory, opts *Options) error {
	return cmdutil.ForEachCity(ctx, f, func(cf *cmdutil.Factory, _ *config.CityConfig) error {
		cityOpts := *opts
		cityOpts.CurrentCity = cf.CurrentCity
		cityOpts.CityDB = cf.CityDB
		return runIngest(ctx, &cityOpts)
	})
}

func runIngest(ctx context.Context, opts *Options) error {
	ios := opts.IO

	city, err := opts.CurrentCity()
	if err != nil {
		return fmt.Errorf("city: %w", err)
	}

	client, err := opts.HttpClient()
	if err != nil {
		return fmt.Errorf("http client: %w", err)
	}

	// Validate --source flag early (name only — bbox filled in after boundary fetch)
	validSourceNames := map[string]bool{"all": true, "overpass": true, "arcgis": true}
	if !validSourceNames[opts.Source] {
		return &cmdutil.FlagError{Err: fmt.Errorf("unknown source %q, valid sources: overpass, arcgis, all", opts.Source)}
	}

	store, err := opts.CityDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	// Fetch city boundary from Nominatim (cached: skip if already stored)
	boundaryGJSON, _ := store.GetBoundary(ctx)
	if boundaryGJSON == "" || opts.Force {
		fmt.Fprintf(ios.Out, "Fetching boundary for %s from Nominatim...\n", city.Name)
		boundaryGJSON, err = ingestpkg.FetchCityBoundary(ctx, client, city.Name)
		if err != nil {
			return fmt.Errorf("fetch boundary: %w", err)
		}
		if err := store.SaveBoundary(ctx, boundaryGJSON, "nominatim"); err != nil {
			return fmt.Errorf("save boundary: %w", err)
		}
		fmt.Fprintf(ios.Out, "  Boundary saved.\n")
	} else {
		fmt.Fprintf(ios.Out, "Using cached boundary for %s (use --force to refresh).\n", city.Name)
	}

	// Derive bbox from boundary polygon
	bbox, err := geo.BBoxFromGeoJSON(boundaryGJSON)
	if err != nil {
		return fmt.Errorf("derive bbox: %w", err)
	}

	arcgisURL := city.ArcGISURL

	var sources []ingestpkg.Source
	if opts.Source == "all" {
		sources = ingestpkg.AllSources(bbox, arcgisURL)
	} else {
		src, _ := ingestpkg.SourceByName(opts.Source, bbox, arcgisURL)
		sources = []ingestpkg.Source{src}
	}

	var allFeatures []db.Feature
	for _, src := range sources {
		fmt.Fprintf(ios.Out, "Fetching %s data from %s for %s...\n", opts.ResourceType.Name(), src.Name(), city.Name)
		features, err := src.Fetch(ctx, client, opts.ResourceType)
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
	if err := store.UpsertFeatures(ctx, opts.ResourceType.Name(), allFeatures); err != nil {
		return fmt.Errorf("save features: %w", err)
	}

	fmt.Fprintf(ios.Out, "Done. Ingested %d %s features.\n", len(allFeatures), opts.ResourceType.Name())
	return nil
}
