package ingest

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/geo"
	ingestpkg "github.com/jcrussell/solvent-streets/internal/ingest"
	"github.com/jcrussell/solvent-streets/internal/logs"
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
	ResourceType resource.Source
	Source       cmdutil.Source
	Force        bool
	DryRun       bool
}

func NewCmdIngest(f *cmdutil.Factory, rt resource.Source, runF func(context.Context, *Options) error) *cobra.Command {
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
		Short: fmt.Sprintf("Ingest %s data from APIs", rt.Type()),
		Example: fmt.Sprintf(`  # Pull %s from the OpenStreetMap Overpass API
  pvmt %s ingest --source overpass

  # Bypass the 24h HTTP cache and re-fetch
  pvmt %s ingest --force

  # Show what would be fetched without making any requests
  pvmt %s ingest --dry-run`, rt.Type(), rt.Type(), rt.Type(), rt.Type()),
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runIngestAllCities(cmd.Context(), f, opts)
		},
	}

	cmd.Flags().Var(&opts.Source, "source", "Data source (overpass|arcgis|all)")
	_ = cmd.RegisterFlagCompletionFunc("source", cmdutil.SourceCompletion())
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Bypass HTTP cache")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Resolve sources and print plan without fetching or writing")

	// --dry-run never fetches, so --force (bypass cache) is meaningless
	// in that combination. Reject the typo with cobra's flag-group helper
	// rather than silently ignoring one (byob-command-shape.6).
	cmd.MarkFlagsMutuallyExclusive("force", "dry-run")

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

	boundaryGJSON, err := resolveBoundary(ctx, opts, store, client, city, ingestpkg.FetchCityBoundary, ingestpkg.FetchCityBoundaryFromRelation)
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
		logs.From(ctx).Warn("no features fetched", "resource", opts.ResourceType.Type())
		return cmdutil.ErrNoResults
	}

	logs.From(ctx).Info("saving features to database", "count", len(allFeatures), "resource", opts.ResourceType.Type())
	if err := store.UpsertFeatures(ctx, opts.ResourceType.Type(), allFeatures); err != nil {
		return fmt.Errorf("save features: %w", err)
	}

	logs.From(ctx).Info("ingest complete", "count", len(allFeatures), "resource", opts.ResourceType.Type())
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
	fmt.Fprintf(out, "[dry-run] ingest %s for %s\n", opts.ResourceType.Type(), city.Name)
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

// nominatimFetcher abstracts the by-name Nominatim search so tests
// can inject canned responses without spinning up an httptest server
// inside the consumer test file. Narrow per byob-interfaces.1
// (define in consumer, not in internal/ingest).
type nominatimFetcher func(ctx context.Context, client *http.Client, cityName string) (string, error)

// relationFetcher abstracts the Overpass-relation-by-id fetch with
// the same shape and rationale as nominatimFetcher.
type relationFetcher func(ctx context.Context, client *http.Client, relationID int64) (string, error)

// resolveBoundary returns the cached city boundary or fetches a fresh
// one. GetBoundary returns ("", nil) on sql.ErrNoRows — any returned
// error is a real DB problem and must surface.
//
// Fetch source selection: if city.BoundaryRelationID is set, fetch
// the admin boundary by relation ID via Overpass (the escape hatch
// for cities like Albuquerque whose boundary lives only in OSM, not
// in Nominatim's index). Otherwise, do the usual Nominatim search by
// city name.
//
// Fresh fetches subtract OSM `natural=water` polygons from the
// boundary so cross-city % paved is apples-to-apples. Soft water-strip
// failures are warned + fallback; over-subtraction is a HARD error.
func resolveBoundary(
	ctx context.Context,
	opts *Options,
	store db.Store,
	client *http.Client,
	city *config.CityConfig,
	fetchByName nominatimFetcher,
	fetchByRelation relationFetcher,
) (string, error) {
	boundaryGJSON, err := store.GetBoundary(ctx)
	if err != nil {
		return "", fmt.Errorf("reading cached boundary: %w", err)
	}
	if boundaryGJSON != "" && !opts.Force {
		fmt.Fprintf(opts.IO.ErrOut, "Using cached boundary for %s (use --force to refresh).\n", city.Name)
		return boundaryGJSON, nil
	}

	var source string
	switch {
	case city.BoundaryRelationID > 0:
		fmt.Fprintf(opts.IO.ErrOut, "Fetching boundary for %s from OSM relation %d via Overpass...\n",
			city.Name, city.BoundaryRelationID)
		boundaryGJSON, err = fetchByRelation(ctx, client, city.BoundaryRelationID)
		source = "overpass-relation"
	default:
		fmt.Fprintf(opts.IO.ErrOut, "Fetching boundary for %s from Nominatim...\n", city.Name)
		boundaryGJSON, err = fetchByName(ctx, client, city.Name)
		source = "nominatim"
	}
	if err != nil {
		return "", cmdutil.Hintf(fmt.Errorf("fetch boundary: %w", err),
			"If Nominatim returned a non-Polygon result, set [[cities]].boundary_relation_id "+
				"to the OSM admin_level=8 relation for this city. Find it with: "+
				"https://overpass-turbo.eu/ → "+
				`relation["name"="<city>"]["boundary"="administrative"]["admin_level"="8"];out;`)
	}

	stripped, warn, stripErr := stripWaterFromBoundary(ctx, client, ingestpkg.FetchOSMWater, boundaryGJSON)
	if stripErr != nil {
		return "", cmdutil.Hintf(fmt.Errorf("water strip for %s: %w", city.Name, stripErr),
			"This usually means the OSM water-stitching pipeline produced a polygon "+
				"covering more than the city boundary. Re-run with verbose logging "+
				"(check stderr for 'water way:', 'water relation:', or 'water coastline:' "+
				"rejection warnings) to find the offending OSM way or relation id, then "+
				"open an issue with the city name, bbox, and the warning text.")
	}
	if warn != "" {
		fmt.Fprintf(opts.IO.ErrOut, "  %s\n", warn)
	}
	if stripped != "" {
		boundaryGJSON = stripped
		source += "+osm-water"
	}

	if err := store.SaveBoundary(ctx, boundaryGJSON, source); err != nil {
		return "", fmt.Errorf("save boundary: %w", err)
	}
	fmt.Fprintf(opts.IO.ErrOut, "  Boundary saved (source=%s).\n", source)
	return boundaryGJSON, nil
}

// waterStripMinAreaRatio is the lower bound on stripped-to-original
// boundary area that we accept from the OSM water subtraction. This is
// the BACKSTOP guard — the per-polygon validation in
// internal/ingest/water.go (acceptWaterPolygon) plus the clip-to-
// boundary intersection in internal/geo/subtract.go are the primary
// defenses against mis-stitched water polygons. The aggregate guard
// only fires when the stripped boundary is reduced to a sliver despite
// those primary defenses passing — almost always a sign the entire
// pipeline is producing garbage rather than a single rogue polygon.
//
// Calibrated against Boston: Boston's Nominatim boundary legitimately
// contains ~50% harbor water (Boston has municipal jurisdiction over
// Boston Harbor, the Inner Harbor, parts of Massachusetts Bay, etc.),
// so a correct strip leaves ~50% of original area as land. A threshold
// of 0.1 means we abort only when 90%+ of the boundary was subtracted,
// well past any plausible real city. Tune up if a future city
// genuinely strips below 10% (it shouldn't); tune down if false
// positives appear on cities with extreme water ratios.
const waterStripMinAreaRatio = 0.1

// ErrWaterStripOverSubtracted signals that the post-subtraction
// boundary is too small relative to the original, which almost always
// indicates a mis-stitched OSM water polygon (or the union of several)
// covering far more area than actual water. Sentinel so callers can
// errors.Is and so tests can pin the failure mode.
var ErrWaterStripOverSubtracted = errors.New("water strip over-subtracted")

// waterFetcher abstracts the OSM water fetch so tests can inject a
// fake response without spinning up an httptest server inside the
// internal/ingest package. The interface is intentionally narrow per
// byob-interfaces.1 (define in consumer, not in internal/ingest).
type waterFetcher func(ctx context.Context, client *http.Client, bbox [4]float64) (string, error)

// stripWaterFromBoundary tries to subtract OSM water from boundaryGJSON.
//
// Returns:
//   - (gjson, "", nil)  - success; use stripped boundary, caller appends
//     "+osm-water" to its existing source label
//   - ("", "", nil)     - no water in bbox; use unstripped boundary
//   - ("", warn, nil)   - soft failure (network/geometry); log warn and fall back
//   - ("", "", err)     - HARD failure (over-subtraction guard); abort
//
// The err return wraps ErrWaterStripOverSubtracted with diagnostic
// detail (areas + ratio); callers should attach the city name via
// ErrHint so the operator can identify which city is affected.
//
// The source label is composed by the caller (resolveBoundary) so the
// upstream fetch (nominatim vs overpass-relation) is preserved in the
// final label — e.g., "nominatim+osm-water" or "overpass-relation+osm-water".
func stripWaterFromBoundary(
	ctx context.Context,
	client *http.Client,
	fetchWater waterFetcher,
	boundaryGJSON string,
) (gjson, warn string, err error) {
	bbox, err := geo.BBoxFromGeoJSON(boundaryGJSON)
	if err != nil {
		return "", fmt.Sprintf("water strip skipped: bbox: %v", err), nil
	}
	waterGJSON, err := fetchWater(ctx, client, bbox)
	if err != nil {
		return "", fmt.Sprintf("water strip skipped: overpass: %v", err), nil
	}
	if waterGJSON == "" {
		return "", "", nil
	}
	stripped, err := geo.SubtractGeoJSON(boundaryGJSON, waterGJSON)
	if err != nil {
		return "", fmt.Sprintf("water strip skipped: subtract: %v", err), nil
	}
	origArea, errOrig := geo.BoundaryAreaSqM(boundaryGJSON)
	stripArea, errStrip := geo.BoundaryAreaSqM(stripped)
	if errOrig == nil && errStrip == nil && !acceptStripRatio(origArea, stripArea, waterStripMinAreaRatio) {
		return "", "", fmt.Errorf("%w: stripped %.0f sq m is %.1f%% of original %.0f sq m",
			ErrWaterStripOverSubtracted, stripArea, 100*stripArea/origArea, origArea)
	}
	return stripped, "", nil
}

// acceptStripRatio returns true iff the stripped boundary is at least
// `threshold` fraction of the original. Extracted as a pure helper so
// the ratio guard can be unit-tested without httptest, geometry, or
// network plumbing. A zero or negative original area is treated as a
// degenerate input that can't be ratio-checked — accept it and let
// downstream surface the real problem.
func acceptStripRatio(orig, stripped, threshold float64) bool {
	if orig <= 0 {
		return true
	}
	return stripped/orig >= threshold
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
		fmt.Fprintf(opts.IO.ErrOut, "Fetching %s data from %s for %s...\n", opts.ResourceType.Type(), src.Name(), cityName)
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
		return nil, fmt.Errorf("%s: %w", opts.ResourceType.Type(), cmdutil.ErrAllSourcesFailed)
	}
	return allFeatures, nil
}
