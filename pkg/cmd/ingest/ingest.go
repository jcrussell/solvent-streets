package ingest

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/geo"
	ingestpkg "github.com/jcrussell/solvent-streets/internal/ingest"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

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
	DryRun       bool
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
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Resolve sources and print plan without fetching or writing")

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

	if opts.DryRun {
		return printDryRunPlan(ctx, opts, store, city)
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

// printDryRunPlan reports what runIngest would do without making any
// network calls or writing to the DB. Reachability probing is
// intentionally omitted: a dry-run that hits the network is slow and
// can fail for reasons unrelated to the config the user is validating
// (transient outages, rate limits). For reachability checks, run the
// real ingest with --source <single-source>.
func printDryRunPlan(ctx context.Context, opts *Options, store db.Store, city *config.CityConfig) error {
	out := opts.IO.Out
	fmt.Fprintf(out, "[dry-run] ingest %s for %s\n", opts.ResourceType.Name(), city.Name)
	boundary, err := store.GetBoundary(ctx)
	if err != nil {
		return fmt.Errorf("reading cached boundary: %w", err)
	}
	if boundary == "" || opts.Force {
		fmt.Fprintf(out, "[dry-run]   would fetch boundary from Nominatim for %q\n", city.Name)
	} else {
		fmt.Fprintln(out, "[dry-run]   boundary cached, would skip Nominatim")
	}
	if opts.Source == cmdutil.SourceAll {
		fmt.Fprintln(out, "[dry-run]   would resolve sources: overpass (always)"+
			fmtArcgis(city.ArcGISURL))
		fmt.Fprintln(out, "[dry-run]   would fetch + dedupe across all sources")
	} else {
		fmt.Fprintf(out, "[dry-run]   would fetch from source: %s\n", opts.Source)
		if string(opts.Source) == "arcgis" && city.ArcGISURL == "" {
			fmt.Fprintln(out, "[dry-run]   WARNING: arcgis selected but city.arcgis_url is empty")
		}
	}
	return nil
}

func fmtArcgis(url string) string {
	if url == "" {
		return " (arcgis: skipped — no arcgis_url for this city)"
	}
	return ", arcgis (" + url + ")"
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
