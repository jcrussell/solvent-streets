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
	Source       cmdutil.Source
	Force        bool
}

func NewCmdIngest(f *cmdutil.Factory, rt resource.ResourceType, runF func(*Options) error) *cobra.Command {
	opts := &Options{
		IO:           f.IOStreams,
		CityDB:       f.CityDB,
		CurrentCity:  f.CurrentCity,
		HttpClient:   f.HttpClient,
		ResourceType: rt,
		Source:       cmdutil.SourceAll,
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

	cmd.Flags().Var(&opts.Source, "source", "Data source (overpass|arcgis|all)")
	_ = cmd.RegisterFlagCompletionFunc("source", cmdutil.SourceCompletion())
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

	store, err := opts.CityDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	boundaryGJSON, err := resolveBoundary(ctx, opts, store, client, city)
	if err != nil {
		return err
	}

	bbox, err := geo.BBoxFromGeoJSON(boundaryGJSON)
	if err != nil {
		return fmt.Errorf("derive bbox: %w", err)
	}

	sources, err := resolveSources(opts, bbox, city.ArcGISURL, ios)
	if err != nil {
		return err
	}

	allFeatures, err := fetchFromSources(ctx, sources, client, opts, city.Name)
	if err != nil {
		return err
	}
	if len(allFeatures) == 0 {
		fmt.Fprintf(ios.ErrOut, "No features fetched for %s\n", opts.ResourceType.Name())
		return cmdutil.ErrNoResults
	}

	fmt.Fprintf(ios.ErrOut, "Saving %d features to database...\n", len(allFeatures))
	if err := store.UpsertFeatures(ctx, opts.ResourceType.Name(), allFeatures); err != nil {
		return fmt.Errorf("save features: %w", err)
	}

	fmt.Fprintf(ios.ErrOut, "Done. Ingested %d %s features.\n", len(allFeatures), opts.ResourceType.Name())
	return nil
}

// resolveBoundary returns the cached city boundary or fetches it from
// Nominatim. GetBoundary returns ("", nil) on sql.ErrNoRows — any
// returned error is a real DB problem and must surface.
func resolveBoundary(ctx context.Context, opts *Options, store db.Store, client *http.Client, city *config.CityConfig) (string, error) {
	boundaryGJSON, err := store.GetBoundary(ctx)
	if err != nil {
		return "", fmt.Errorf("reading cached boundary: %w", err)
	}
	if boundaryGJSON != "" && !opts.Force {
		fmt.Fprintf(opts.IO.ErrOut, "Using cached boundary for %s (use --force to refresh).\n", city.Name)
		return boundaryGJSON, nil
	}
	fmt.Fprintf(opts.IO.ErrOut, "Fetching boundary for %s from Nominatim...\n", city.Name)
	boundaryGJSON, err = ingestpkg.FetchCityBoundary(ctx, client, city.Name)
	if err != nil {
		return "", fmt.Errorf("fetch boundary: %w", err)
	}
	if err := store.SaveBoundary(ctx, boundaryGJSON, "nominatim"); err != nil {
		return "", fmt.Errorf("save boundary: %w", err)
	}
	fmt.Fprintf(opts.IO.ErrOut, "  Boundary saved.\n")
	return boundaryGJSON, nil
}

func resolveSources(opts *Options, bbox [4]float64, arcgisURL string, ios *iostreams.IOStreams) ([]ingestpkg.Source, error) {
	srcOpts := ingestpkg.Options{Progress: ios.ErrOut}
	if opts.Source == cmdutil.SourceAll {
		return ingestpkg.AllSources(bbox, arcgisURL, srcOpts), nil
	}
	src, err := ingestpkg.SourceByName(string(opts.Source), bbox, arcgisURL, srcOpts)
	if err != nil {
		return nil, fmt.Errorf("resolving source %q: %w", opts.Source, err)
	}
	return []ingestpkg.Source{src}, nil
}

func fetchFromSources(ctx context.Context, sources []ingestpkg.Source, client *http.Client, opts *Options, cityName string) ([]db.Feature, error) {
	var allFeatures []db.Feature
	failures := 0
	for _, src := range sources {
		fmt.Fprintf(opts.IO.ErrOut, "Fetching %s data from %s for %s...\n", opts.ResourceType.Name(), src.Name(), cityName)
		features, err := src.Fetch(ctx, client, opts.ResourceType)
		if err != nil {
			fmt.Fprintf(opts.IO.ErrOut, "Warning: %s fetch failed: %v\n", src.Name(), err)
			failures++
			continue
		}
		fmt.Fprintf(opts.IO.ErrOut, "  Got %d features from %s\n", len(features), src.Name())
		allFeatures = append(allFeatures, features...)
	}
	if len(sources) > 0 && failures == len(sources) {
		return nil, fmt.Errorf("%s: %w", opts.ResourceType.Name(), cmdutil.ErrAllSourcesFailed)
	}
	return allFeatures, nil
}
