package ingest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jcrussell/solvent-streets/internal/cache"
	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/hint"
	ingestpkg "github.com/jcrussell/solvent-streets/internal/ingest"
	"github.com/jcrussell/solvent-streets/internal/logs"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Options struct {
	IO          *iostreams.IOStreams
	CityDB      func() (db.Store, error)
	CurrentCity func() (*config.CityConfig, error)
	HttpClient  func() (*http.Client, error)
	// OverpassURL is the factory's cmdutil.Factory.OverpassURL; nil uses
	// ingest.DefaultOverpassURL.
	OverpassURL  func() (string, error)
	ResourceType resource.Source
	Source       cmdutil.Source
	Force        bool
	DryRun       bool
	// RebuildBoundary re-resolves a stored boundary through the HTTP cache.
	// --force implies it (see rebuildsBoundary).
	RebuildBoundary bool

	boundary *Boundary // resolved up front by `all ingest`; nil resolves it per run

	// overpassURL is OverpassURL's value, resolved by resolveOverpassURL
	// before the run's first fetch; empty means ingest.DefaultOverpassURL.
	overpassURL string
}

// resolveOverpassURL resolves o.OverpassURL into o.overpassURL. It runs
// before the first fetch, so a non-default endpoint that fails the SSRF guard
// stops the run before any Overpass request is sent.
func (o *Options) resolveOverpassURL() error {
	if o.OverpassURL == nil {
		return nil
	}
	u, err := o.OverpassURL()
	if err != nil {
		return err
	}
	o.overpassURL = u
	return nil
}

func NewCmdIngest(f *cmdutil.Factory, rt resource.Source, runF func(context.Context, *Options) error) *cobra.Command {
	opts := &Options{
		IO:           f.IOStreams,
		CityDB:       f.CityDB,
		CurrentCity:  f.CurrentCity,
		HttpClient:   f.HttpClient,
		OverpassURL:  f.OverpassURL,
		ResourceType: rt,
		Source:       cmdutil.SourceAll,
	}

	cmd := &cobra.Command{
		Use:     "ingest",
		Args:    cobra.NoArgs,
		Short:   Short(string(rt.Type())),
		Long:    Long(string(rt.Type()), string(rt.Type()), rt),
		Example: Example(string(rt.Type()), string(rt.Type()), rt),
		RunE: func(cmd *cobra.Command, args []string) error {
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runIngestAllCities(cmd.Context(), f, opts)
		},
	}

	source := &cmdutil.SourceFlag{Value: &opts.Source, Valid: sourceValues(rt)}
	cmd.Flags().Var(source, "source", "Data source ("+strings.Join(source.Valid, "|")+")")
	_ = cmd.RegisterFlagCompletionFunc("source", source.Completion())
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Bypass the HTTP cache and re-fetch the city boundary")
	cmd.Flags().BoolVar(&opts.RebuildBoundary, "rebuild-boundary", false, "Re-resolve the stored city boundary and its water strip, through the HTTP cache")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Resolve sources and print the plan without fetching anything or storing features")

	// --dry-run never fetches, so --force (bypass cache) is meaningless
	// in that combination. Reject the typo with cobra's flag-group helper
	// rather than silently ignoring one.
	cmd.MarkFlagsMutuallyExclusive("force", "dry-run")

	return cmd
}

// sourceValues returns the --source values rt accepts. A pavement resource
// takes "all" or either configurable source (cmdutil.PavementSources). An
// own-sourced resource (resource.OwnSourced: census blocks from tigerweb,
// facilities from overpass) takes "all" or its own source's name, both
// meaning that one source; any other value cannot apply to it.
func sourceValues(rt resource.Source) []string {
	if own, ok := rt.(resource.OwnSourced); ok {
		return []string{string(cmdutil.SourceAll), own.OwnSource()}
	}
	return cmdutil.PavementSources()
}

// Short is the one-line help of the ingest subcommand of a command group
// whose features are noun ("roads", "Census blocks").
func Short(noun string) string {
	return fmt.Sprintf("Fetch %s for each configured city", noun)
}

// sourceNames are the upstream services as help prose names them.
var sourceNames = map[string]string{
	resource.SourceOverpass: "Overpass",
	resource.SourceTigerweb: "TIGERweb",
	resource.SourceArcGIS:   "ArcGIS",
}

// sourceName is source as help prose names it.
func sourceName(source string) string {
	if n, ok := sourceNames[source]; ok {
		return n
	}
	return source
}

// Long is the --help description of the ingest subcommand of the command
// group named group, whose features are noun. Its source sentence depends on
// whether rt has its own fixed source or reads the city's overpass /
// arcgis_url settings.
func Long(group, noun string, rt resource.Source) string {
	var src string
	if own, ok := rt.(resource.OwnSourced); ok {
		src = fmt.Sprintf("from %s (its only source; the city's overpass and arcgis_url settings do not apply)", sourceName(own.OwnSource()))
	} else {
		src = "from the city's configured sources: OpenStreetMap via Overpass when overpass = true, and the agency ArcGIS layer when arcgis_url is set"
	}
	return cmdutil.Reflow(fmt.Sprintf(`Fetch %s for every configured city, or the one --city names,
%s, and store them in the local database. Rows from each
source that answered replace that source's previous rows; a source that
fails leaves its rows in place.

The city boundary is resolved first, from boundary_relation_id when set and
otherwise by name from Nominatim, with open water stripped, and stored for
every later step. Resolving a boundary also fetches the city's roads, from
every configured source and over the unstripped boundary, as land evidence
for the water strip, and stores them; a roads ingest then reports those
instead of fetching again. HTTP responses are cached; --force bypasses the cache and
re-fetches the boundary. --rebuild-boundary re-resolves a stored boundary
through the cache without bypassing it, so a change to the water strip can
be replayed; any request the cache does not hold (such as roads over an
unstripped bbox no earlier ingest queried) goes upstream.

Run 'pvmt %s compute' next.`, noun, src, group), cmdutil.HelpWidth)
}

// Example is the --help example text for the ingest subcommand of the
// command group named group (`pvmt <group> ingest`), whose features are noun.
// The group name is not derivable from rt: census blocks are stored as
// "layer-census-blocks" and fire stations as "facility-fire", but their
// commands are `pvmt census` and `pvmt services`. A resource with one fixed
// source gets no --source example.
func Example(group, noun string, rt resource.Source) string {
	var b strings.Builder
	if own, ok := rt.(resource.OwnSourced); ok {
		fmt.Fprintf(&b, "  # Ingest every configured city (%s come from %s only)\n  pvmt %s ingest\n\n",
			noun, sourceName(own.OwnSource()), group)
	} else {
		fmt.Fprintf(&b, "  # Pull %s from the OpenStreetMap Overpass API only\n  pvmt %s ingest --source overpass\n\n",
			noun, group)
	}
	fmt.Fprintf(&b, `  # Bypass the HTTP cache and re-fetch
  pvmt %s ingest --force

  # Re-resolve the stored boundary through the HTTP cache
  pvmt %s ingest --rebuild-boundary

  # Show what would be fetched without making any requests
  pvmt %s ingest --dry-run`, group, group, group)
	return b.String()
}

// RunResourceForCity ingests a single resource type against the already
// city-scoped factory f. `all ingest` calls it (as RunResourceWithBoundary)
// and the fire and Census steps call it in-process — instead of
// building a throwaway ingest cobra command and re-parsing flags through it —
// so the fan-out uses the same mechanism as `all compute`'s
// compute.RunResourceForCity. It mirrors the Options NewCmdIngest builds at
// their flag defaults: `all ingest` registers no --source/--force/--dry-run
// flags of its own, so there is nothing to forward and Source is pinned to
// the same cmdutil.SourceAll default the flag would have supplied.
func RunResourceForCity(ctx context.Context, f *cmdutil.Factory, rt resource.Source) error {
	return RunResourceWithBoundary(ctx, f, rt, nil)
}

// Boundary is a city's boundary resolved once by ResolveBoundaryForCity and
// shared by the ingest steps that follow, so `pvmt all ingest` fetches the
// boundary and its water strip once per city instead of once per step. The
// boundary itself lives in the store; Boundary carries what resolving it
// did that a later step reports: a fresh boundary fetches and saves the
// city's roads (fetchBoundary), so the roads step has nothing left to fetch.
type Boundary struct {
	roads *roadsOutcome // nil on a cache hit, or when no roads were fetched
}

// boundaryRoads returns the roads b's resolution saved, or nil (also for a
// nil b).
func (b *Boundary) boundaryRoads() *roadsOutcome {
	if b == nil {
		return nil
	}
	return b.roads
}

// roadsOutcome is the roads a fresh boundary's resolution fetched, over the
// unstripped boundary's bbox, and saved, over the final boundary's bbox.
type roadsOutcome struct {
	saved     int
	succeeded []string // sources that answered
	failed    []string // sources that did not
	err       error    // the save failed
}

// result is what the roads step reports for roads the boundary step saved,
// matching what fetchAndSave reports for a fetch of its own: the save error,
// ErrNoResults (or ErrPartialIngest when a source failed) for nothing saved,
// ErrPartialIngest naming a failed source, or nil.
func (o *roadsOutcome) result() error {
	switch {
	case o.err != nil:
		return o.err
	case o.saved == 0:
		return emptyFetchErr(resource.TypeRoads, o.failed, o.succeeded)
	case len(o.failed) > 0:
		return fmt.Errorf("%s: %s failed, saved %d features from %s: %w",
			resource.TypeRoads, strings.Join(o.failed, ", "), o.saved,
			strings.Join(o.succeeded, ", "), cmdutil.ErrPartialIngest)
	}
	return nil
}

// ResolveBoundaryForCity returns the city's cached boundary or fetches and
// saves a fresh one, exactly as the first step of a plain ingest would. Pass
// the result to RunResourceWithBoundary for each step. An error means the
// city has no usable boundary, so every one of its ingest steps would fail
// the same way. rebuild re-resolves a boundary the store already holds, as
// --rebuild-boundary does for a plain ingest. It does not bypass the HTTP
// cache: for --force (which implies rebuild) the caller marks ctx with
// cache.WithBypass.
func ResolveBoundaryForCity(ctx context.Context, f *cmdutil.Factory, rebuild bool) (*Boundary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	opts := &Options{IO: f.IOStreams, OverpassURL: f.OverpassURL, Source: cmdutil.SourceAll, RebuildBoundary: rebuild}
	city, err := f.CurrentCity()
	if err != nil {
		return nil, err
	}
	if err := opts.resolveOverpassURL(); err != nil {
		return nil, err
	}
	client, err := f.HttpClient()
	if err != nil {
		return nil, fmt.Errorf("http client: %w", err)
	}
	store, err := f.CityDB()
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	_, b, err := resolveBoundary(withRetryNotices(ctx, opts.IO), opts, store, client, city, liveBoundaryFetchers(opts, city))
	if err != nil {
		return nil, err
	}
	if b == nil {
		b = &Boundary{}
	}
	return b, nil
}

// RunResourceWithBoundary is RunResourceForCity for a city whose boundary
// ResolveBoundaryForCity already resolved; b may be nil, which resolves it
// here.
func RunResourceWithBoundary(ctx context.Context, f *cmdutil.Factory, rt resource.Source, b *Boundary) error {
	// Pre-check cancellation before doing any work. The `all` fan-out calls
	// this in-process, not through a cobra command, so cmdutil.ForEachCity's
	// pre-check does not cover it, and without this check a mid-fan-out
	// cancellation aborts only because the store or HTTP layer happens to
	// surface context.Canceled. Real sqlite does; a ctx-ignoring store —
	// dbtest.MockStore — would run every remaining resource after cancellation.
	if err := ctx.Err(); err != nil {
		return err
	}
	opts := &Options{
		IO:           f.IOStreams,
		CityDB:       f.CityDB,
		CurrentCity:  f.CurrentCity,
		HttpClient:   f.HttpClient,
		OverpassURL:  f.OverpassURL,
		ResourceType: rt,
		Source:       cmdutil.SourceAll,
		DryRun:       IsDryRun(ctx),
		boundary:     b,
	}
	return runIngest(ctx, opts)
}

type dryRunKey struct{}

// dryRunState is what a dry run shared by several steps remembers: the
// cities whose boundary plan it has printed, so each prints once.
type dryRunState struct {
	boundaryShown map[string]bool
}

// WithDryRun marks ctx so every ingest step run through it
// (RunResourceForCity, RunResourceWithBoundary) prints its plan instead of
// fetching, as --dry-run does for one resource: `pvmt all ingest --dry-run`.
func WithDryRun(ctx context.Context) context.Context {
	return context.WithValue(ctx, dryRunKey{}, &dryRunState{boundaryShown: map[string]bool{}})
}

// withDryRunState is ctx marked by WithDryRun, unless it already is.
func withDryRunState(ctx context.Context) context.Context {
	if IsDryRun(ctx) {
		return ctx
	}
	return WithDryRun(ctx)
}

// IsDryRun reports whether ctx is marked by WithDryRun.
func IsDryRun(ctx context.Context) bool {
	_, ok := ctx.Value(dryRunKey{}).(*dryRunState)
	return ok
}

// firstBoundaryPlan reports whether the dry run on ctx has not yet printed
// city's boundary plan, and records that it now has.
func firstBoundaryPlan(ctx context.Context, city *config.CityConfig) bool {
	st, ok := ctx.Value(dryRunKey{}).(*dryRunState)
	if !ok {
		return true
	}
	if st.boundaryShown[city.Slug()] {
		return false
	}
	st.boundaryShown[city.Slug()] = true
	return true
}

func runIngestAllCities(ctx context.Context, f *cmdutil.Factory, opts *Options) error {
	if err := checkSourceForCities(f, opts); err != nil {
		return err
	}
	return cmdutil.ForEachCity(ctx, f, func(ctx context.Context, cf *cmdutil.Factory, _ *config.CityConfig) error {
		cityOpts := *opts
		cityOpts.CurrentCity = cf.CurrentCity
		cityOpts.CityDB = cf.CityDB
		return runIngest(ctx, &cityOpts)
	})
}

func runIngest(ctx context.Context, opts *Options) error {
	city, err := opts.CurrentCity()
	if err != nil {
		return err
	}
	if err := censusOptOut(opts, city); err != nil {
		return err
	}

	store, err := opts.CityDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	if opts.DryRun {
		ctx = withDryRunState(ctx)
		return ingestCompanions(opts, printDryRunPlan(ctx, opts, store, city), func(o *Options) error {
			return printDryRunPlan(ctx, o, store, city)
		})
	}

	client, err := opts.HttpClient()
	if err != nil {
		return fmt.Errorf("http client: %w", err)
	}

	// --force is documented as "bypass HTTP cache". Mark the context so
	// the outermost cache.CachingTransport skips serving/revalidating
	// cached GET responses (it still writes the fresh ones back). All
	// fetches below derive from this ctx, and stdlib propagates it across
	// redirects, so every hop is bypassed too.
	if opts.Force {
		ctx = cache.WithBypass(ctx)
	}
	ctx = withRetryNotices(ctx, opts.IO)
	if err := opts.resolveOverpassURL(); err != nil {
		return err
	}

	// A --source this city's pvmt.toml switched off fails before the
	// boundary fetch, not after it. The dry run above
	// only warns about it.
	roads, err := fetchAndSave(ctx, opts, store, client, city)
	err = ingestCompanions(opts, err, func(o *Options) error {
		// The resource's own run resolved the boundary, so a companion
		// reads it from the store rather than re-resolving it for
		// --force or --rebuild-boundary.
		if o.boundary == nil {
			o.boundary = &Boundary{}
		}
		_, err := fetchAndSave(ctx, o, store, client, city)
		return err
	})
	// The roads a fresh boundary saved are reported after the companions,
	// which do not depend on them: a failed roads save must not keep the
	// routing roads from being fetched.
	return reportBoundaryRoads(opts.IO, city, roads, err)
}

// ingestCompanions runs run for each of opts.ResourceType's companions
// (resource.Companioned) with the same options, after the resource's own
// ingest returned err. A failed own ingest stops there; one that found
// nothing (ErrNoResults: a city whose stations are all hand-placed) still
// fetches its companions, and reports ErrNoResults only if they succeed.
func ingestCompanions(opts *Options, err error, run func(*Options) error) error {
	c, ok := opts.ResourceType.(resource.Companioned)
	if !ok || (err != nil && !errors.Is(err, cmdutil.ErrNoResults)) {
		return err
	}
	for _, rt := range c.Companions() {
		o := *opts
		o.ResourceType = rt
		if cerr := run(&o); cerr != nil && !errors.Is(cerr, cmdutil.ErrNoResults) {
			return fmt.Errorf("%s: %w", rt.Type(), cerr)
		}
	}
	return err
}

// fetchAndSave is the network half of runIngest: the source check, the
// boundary, the fetch and the save. A fresh boundary saves the city's roads
// itself (fetchBoundary), so a roads ingest then reports those instead of
// fetching them again. Any other ingest returns, beside its own result, the
// roads its fresh boundary saved, for the caller to report
// (reportBoundaryRoads); nil when the boundary was not fresh in this run.
func fetchAndSave(ctx context.Context, opts *Options, store db.Store, client *http.Client, city *config.CityConfig) (*roadsOutcome, error) {
	if err := checkSourceEnabled(opts, city); err != nil {
		return nil, err
	}
	ios := opts.IO

	boundaryGJSON, b, err := resolveBoundary(ctx, opts, store, client, city, liveBoundaryFetchers(opts, city))
	if err != nil {
		return nil, err
	}
	// Roads a fresh boundary saved in this very run, which a step other than
	// roads reports alongside its own result (reportBoundaryRoads).
	freshRoads := b.boundaryRoads()
	if b == nil {
		// A boundary resolved up front (`all ingest`) makes this run a cache
		// hit, but the roads it saved are still this step's to report.
		b = opts.boundary
	}
	if roads := b.boundaryRoads(); roads != nil && opts.ResourceType.Type() == resource.TypeRoads {
		fmt.Fprintf(ios.ErrOut, "Roads for %s were fetched with the boundary; not fetching them again.\n", city.Name)
		return nil, roads.result()
	}
	return freshRoads, fetchAndSaveResource(ctx, opts, store, client, city, boundaryGJSON)
}

// reportBoundaryRoads folds the roads a fresh boundary saved during a
// non-roads ingest (`pvmt parking ingest` on a new city) into that ingest's
// result err. A failed roads save fails the run, so its exit status says
// so; a road source that failed while the rest saved is a warning with the
// command that retries it. roads is nil when the run's boundary was not
// fresh.
func reportBoundaryRoads(ios *iostreams.IOStreams, city *config.CityConfig, roads *roadsOutcome, err error) error {
	if roads == nil {
		return err
	}
	if len(roads.failed) > 0 && roads.err == nil {
		cmdutil.Warnf(ios, "roads fetched with the boundary: %s failed, saved %d roads from %s (retry: pvmt --city %s roads ingest)",
			strings.Join(roads.failed, ", "), roads.saved, strings.Join(roads.succeeded, ", "), city.Slug())
	}
	if roads.err == nil {
		return err
	}
	rerr := fmt.Errorf("roads fetched with the boundary: %w (retry: pvmt --city %s roads ingest)", roads.err, city.Slug())
	if err == nil || errors.Is(err, cmdutil.ErrNoResults) {
		// ErrNoResults would file the run as empty and swallow the failure.
		return rerr
	}
	return errors.Join(err, rerr)
}

// fetchAndSaveResource is fetchAndSave's own fetch and save of
// opts.ResourceType over boundaryGJSON.
func fetchAndSaveResource(ctx context.Context, opts *Options, store db.Store, client *http.Client, city *config.CityConfig, boundaryGJSON string) error {
	ios := opts.IO

	bbox, err := geo.BBoxFromGeoJSON(boundaryGJSON)
	if err != nil {
		return fmt.Errorf("derive bbox: %w", err)
	}
	// A city whose roads come from Overpass already holds every road that
	// touches its boundary bbox, so the routing roads around it are fetched
	// as the ring outside it (resource.ExcludingQuerier).
	var exclude *[4]float64
	if city.Overpass {
		inner := bbox
		exclude = &inner
	}
	bbox = fetchBBox(opts.ResourceType, bbox)

	// An own-sourced resource (facilities, census blocks) ignores the city's
	// overpass / arcgis_url settings; see resolveSources.
	sources, err := resolveSources(opts, bbox, exclude, city.Overpass, city.ArcGISURL, city.AllowPrivateArcGIS, ios)
	if err != nil {
		return err
	}

	allFeatures, succeeded, attempts, err := fetchFromSources(ctx, sources, client, opts, city.Name)
	if err != nil {
		recordAttempts(ctx, opts, store, attempts)
		return err
	}
	failed := failedSources(sources, succeeded)
	if len(allFeatures) == 0 {
		// Nothing fetched: return without touching the DB so a transient
		// all-empty result never wipes previously-stored rows. (A surviving
		// source that legitimately emptied out thus keeps its stale rows until
		// a clean full re-ingest — preferred over destructive deletion.)
		recordAttempts(ctx, opts, store, attempts)
		logs.From(ctx).Debug("no features fetched", "resource", opts.ResourceType.Type())
		return emptyFetchErr(opts.ResourceType.Type(), failed, succeeded)
	}

	replaceSources := replaceScope(opts.Source, len(sources), succeeded)

	logs.From(ctx).Info("saving features to database", "count", len(allFeatures), "resource", opts.ResourceType.Type())
	if err := store.UpsertFeatures(ctx, opts.ResourceType.Type(), allFeatures, replaceSources); err != nil {
		err = fmt.Errorf("save features: %w", err)
		recordAttempts(ctx, opts, store, failAnswered(attempts, err))
		return err
	}
	recordAttempts(ctx, opts, store, attempts)

	logs.From(ctx).Info("ingest complete", "count", len(allFeatures), "resource", opts.ResourceType.Type())
	if len(failed) > 0 {
		return fmt.Errorf("%s: %s failed, saved %d features from %s: %w",
			opts.ResourceType.Type(), strings.Join(failed, ", "), len(allFeatures),
			strings.Join(succeeded, ", "), cmdutil.ErrPartialIngest)
	}
	return nil
}

// fetchBBox is the bbox rt is fetched over: the boundary's, grown by the
// resource's pad when it has one (resource.FetchPadder: service facilities
// and their routing roads reach past the city line).
func fetchBBox(rt resource.Source, bbox [4]float64) [4]float64 {
	if p, ok := rt.(resource.FetchPadder); ok {
		return geo.PadBBox(bbox, p.FetchPadM())
	}
	return bbox
}

// emptyFetchErr is the result of a fetch that saved nothing. A source that
// failed outranks an empty survivor: reporting ErrNoResults would let
// `pvmt all ingest` skip the layer as empty and exit 0 with it missing.
func emptyFetchErr(rt resource.Type, failed, succeeded []string) error {
	if len(failed) == 0 {
		return cmdutil.ErrNoResults
	}
	return fmt.Errorf("%s: %s failed, %s returned no features: %w",
		rt, strings.Join(failed, ", "), strings.Join(succeeded, ", "), cmdutil.ErrPartialIngest)
}

// failedSources lists the sources that did not answer, in source order.
func failedSources(sources []ingestpkg.Source, succeeded []string) []string {
	var failed []string
	for _, src := range sources {
		if !slices.Contains(succeeded, src.Name()) {
			failed = append(failed, src.Name())
		}
	}
	return failed
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
	showBoundary := firstBoundaryPlan(ctx, city)
	switch src := boundarySourceFor(city); {
	case !showBoundary:
	case boundary == "" || opts.Force:
		fmt.Fprintf(out, "[dry-run]   would fetch boundary for %q from %s\n", city.Name, src.from)
	case opts.RebuildBoundary:
		fmt.Fprintf(out, "[dry-run]   would re-resolve the stored boundary for %q from %s, through the HTTP cache\n", city.Name, src.from)
	default:
		fmt.Fprintf(out, "[dry-run]   boundary cached, would skip %s\n", src.from)
	}
	if showBoundary && (boundary == "" || opts.rebuildsBoundary()) {
		fmt.Fprintln(out, "[dry-run]   would fetch roads over the unstripped boundary bbox as land evidence for the water strip, and save them")
	}
	if p, ok := opts.ResourceType.(resource.FetchPadder); ok {
		fmt.Fprintf(out, "[dry-run]   would fetch over the boundary bbox padded by %.0f m\n", p.FetchPadM())
	}
	if own, ok := opts.ResourceType.(resource.OwnSourced); ok {
		fmt.Fprintf(out, "[dry-run]   would fetch from source: %s (fixed for %s)\n", own.OwnSource(), opts.ResourceType.Type())
	} else if opts.Source == cmdutil.SourceAll {
		fmt.Fprintln(out, "[dry-run]   would resolve sources: "+fmtOverpass(city.Overpass)+
			fmtArcgis(city.ArcGISURL, opts.ResourceType))
		fmt.Fprintln(out, "[dry-run]   would fetch + dedupe across all sources")
	} else {
		fmt.Fprintf(out, "[dry-run]   would fetch from source: %s\n", opts.Source)
		if reason := disabledSourceReason(opts.Source, city.Overpass, city.ArcGISURL); reason != "" {
			cmdutil.Warnf(opts.IO, "--source %s is not available for %s: %s", opts.Source, city.Name, reason)
		}
	}
	return nil
}

func fmtOverpass(overpass bool) string {
	if overpass {
		return string(cmdutil.SourceOverpass)
	}
	return "(overpass: skipped — overpass=false for this city)"
}

// servedTypes names the pavement resources src serves ("roads"), for a
// TypeServer; every resource otherwise.
func servedTypes(src ingestpkg.Source) string {
	ts, ok := src.(ingestpkg.TypeServer)
	var names []string
	for _, rt := range resource.All {
		if !ok || ts.Serves(rt) {
			names = append(names, string(rt.Type()))
		}
	}
	return strings.Join(names, ", ")
}

func fmtArcgis(url string, rt resource.Source) string {
	switch {
	case url == "":
		return " (arcgis: skipped, no arcgis_url for this city)"
	case rt.Type() != resource.TypeRoads:
		return " (arcgis: skipped, it serves only " + servedTypes(&ingestpkg.ArcGISSource{}) + ")"
	}
	return ", arcgis (" + url + ")"
}

// nominatimFetcher abstracts the by-name Nominatim search so tests
// can inject canned responses without spinning up an httptest server
// inside the consumer test file. Narrow.1
// (define in consumer, not in internal/ingest).
type nominatimFetcher func(ctx context.Context, client *http.Client, cityName string) (string, error)

// relationFetcher abstracts the Overpass-relation-by-id fetch with
// the same shape and rationale as nominatimFetcher.
type relationFetcher func(ctx context.Context, client *http.Client, relationID int64) (string, error)

// roadsFetcher fetches the city's roads over bbox from every configured road
// source, as fetchFromSources does. A failed source is in the result, not
// the error; err is non-nil only when every source failed (or the fetch was
// canceled).
type roadsFetcher func(ctx context.Context, client *http.Client, bbox [4]float64) (roadsFetch, error)

// roadsFetch is one roadsFetcher answer.
type roadsFetch struct {
	features  []db.Feature
	sources   []string // every source asked, in order
	succeeded []string
	attempts  []db.IngestAttempt
}

// boundaryFetchers is every network call resolveBoundary makes. All four
// are injected — the water and roads fetches included — so its tests never
// reach Nominatim or Overpass and cannot hang `make test` where the network
// is unreachable.
type boundaryFetchers struct {
	byName     nominatimFetcher
	byRelation relationFetcher
	water      waterFetcher
	roads      roadsFetcher
}

// boundarySource is where a fresh boundary for a city is fetched from. The
// real run fetches through it and the dry run describes it, so the two
// cannot disagree about which upstream a city uses.
type boundarySource struct {
	// name is the source the unstripped boundary is saved under.
	name string
	// from names the upstream in messages.
	from string
	// relationID is the OSM relation to fetch via Overpass; 0 means search
	// Nominatim by city name.
	relationID int64
}

// boundarySourceFor picks the boundary source for city: its
// boundary_relation_id via Overpass when set (the escape hatch for cities
// like Albuquerque whose boundary lives only in OSM, not in Nominatim's
// index), otherwise the Nominatim search by city name.
func boundarySourceFor(city *config.CityConfig) boundarySource {
	if city.BoundaryRelationID > 0 {
		return boundarySource{
			name:       "overpass-relation",
			from:       fmt.Sprintf("OSM relation %d via Overpass", city.BoundaryRelationID),
			relationID: city.BoundaryRelationID,
		}
	}
	return boundarySource{name: "nominatim", from: "Nominatim"}
}

func (s boundarySource) fetch(ctx context.Context, client *http.Client, cityName string, fetch boundaryFetchers) (string, error) {
	if s.relationID > 0 {
		return fetch.byRelation(ctx, client, s.relationID)
	}
	return fetch.byName(ctx, client, cityName)
}

// liveBoundaryFetchers wires the real upstream fetchers. The roads come
// from every road source city configures, whatever --source says: they are
// the water strip's land evidence, and the roads step reuses them.
// Every Overpass fetch among them goes to opts.overpassURL.
func liveBoundaryFetchers(opts *Options, city *config.CityConfig) boundaryFetchers {
	overpassURL := opts.overpassURL
	return boundaryFetchers{
		byName: ingestpkg.FetchCityBoundary,
		byRelation: func(ctx context.Context, client *http.Client, relationID int64) (string, error) {
			return ingestpkg.FetchCityBoundaryFromRelation(ctx, client, overpassURL, relationID)
		},
		water: func(ctx context.Context, client *http.Client, bbox [4]float64, probes ingestpkg.LandProbes) (string, error) {
			return ingestpkg.FetchOSMWater(ctx, client, overpassURL, bbox, probes)
		},
		roads: func(ctx context.Context, client *http.Client, bbox [4]float64) (roadsFetch, error) {
			sources := ingestpkg.AllSources(bbox, city.Overpass, city.ArcGISURL,
				ingestpkg.Options{Progress: opts.IO.ErrOut, AllowPrivateArcGIS: city.AllowPrivateArcGIS, OverpassURL: overpassURL})
			roadOpts := &Options{IO: opts.IO, ResourceType: &resource.Pavement{}, Source: cmdutil.SourceAll}
			features, succeeded, attempts, err := fetchFromSources(ctx, sources, client, roadOpts, city.Name)
			out := roadsFetch{features: features, succeeded: succeeded, attempts: attempts}
			for _, src := range sources {
				out.sources = append(out.sources, src.Name())
			}
			return out, err
		},
	}
}

// rebuildsBoundary reports whether this run re-resolves a boundary the store
// already holds: --rebuild-boundary, or --force, which also bypasses the
// HTTP cache.
// A run whose boundary was already resolved (o.boundary set: by `all ingest`,
// or by the resource a companion rides along with) never rebuilds it again.
func (o *Options) rebuildsBoundary() bool {
	return o.boundary == nil && (o.Force || o.RebuildBoundary)
}

// replaceScope returns the source_api values a save replaces, or nil to
// replace the city's whole resource. Only a run that covered every configured
// source (--source all) and saw each succeed replaces the whole set, which
// also clears any legacy/empty source_api rows. Otherwise the replace is
// scoped to the sources that succeeded: a down source's previously-stored
// rows, and a source the run never asked for (--source arcgis on a city that
// also uses overpass), are preserved rather than wiped.
func replaceScope(requested cmdutil.Source, resolved int, succeeded []string) []string {
	if requested == cmdutil.SourceAll && len(succeeded) == resolved {
		return nil
	}
	return succeeded
}

// resolveBoundary returns the cached city boundary or fetches a fresh
// one. GetBoundary returns ("", nil) when none is stored — any returned
// error is a real DB problem and must surface.
//
// The second return is non-nil exactly when a fresh fetch happened
// (cache miss, --force or --rebuild-boundary), and carries the roads that
// fetch saved (fetchBoundary).
//
// The fetch goes to the city's boundarySourceFor.
func resolveBoundary(
	ctx context.Context,
	opts *Options,
	store db.Store,
	client *http.Client,
	city *config.CityConfig,
	fetch boundaryFetchers,
) (string, *Boundary, error) {
	boundaryGJSON, err := store.GetBoundary(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("reading cached boundary: %w", err)
	}
	if boundaryGJSON != "" && !opts.rebuildsBoundary() {
		fmt.Fprintf(opts.IO.ErrOut, "Using cached boundary for %s (--rebuild-boundary re-resolves it).\n", city.Name)
		return boundaryGJSON, nil, nil
	}

	src := boundarySourceFor(city)
	started := time.Now()
	boundaryGJSON, b, err := fetchBoundary(ctx, opts, store, client, city, src, fetch)
	attempt := db.IngestAttempt{Input: db.InputBoundary, Source: src.name, StartedAt: started, FinishedAt: time.Now(),
		Outcome: db.IngestOK, FeatureCount: 1}
	switch {
	case errors.Is(err, context.Canceled):
	case err != nil:
		attempt.Outcome, attempt.FeatureCount, attempt.Error = db.IngestFailed, 0, err.Error()
		recordAttempts(ctx, opts, store, []db.IngestAttempt{attempt})
	default:
		recordAttempts(ctx, opts, store, []db.IngestAttempt{attempt})
	}
	return boundaryGJSON, b, err
}

// fetchBoundary fetches city's boundary from src, strips water from it and
// saves it: the network half of resolveBoundary.
//
// The city's roads are its land evidence. They are fetched first, from every
// configured road source, over the unstripped boundary's bbox, which holds
// every boundary the strip can produce. Their points inside the unstripped
// boundary are the water strip's land probes (ingest.RoadLandProbes), so a
// water candidate that holds the city's streets is dropped; a city with no
// such roads falls back to the boundary's interior points. The road-coverage
// backstop (chooseBoundary) then checks the stripped boundary against the
// same roads before anything is saved. The boundary is saved once, and the
// roads are saved filtered to the saved boundary's bbox, the rows a fetch
// over that bbox would store, so the roads step has nothing left to fetch.
//
// A failed water or roads FETCH is a HARD error and nothing is saved, so
// the next ingest retries (ErrWaterFetch, ErrRoadsFetch); geometry-side
// strip failures are warned + fallback; over-subtraction, and a boundary
// that holds too few of the city's own roads, are HARD errors.
func fetchBoundary(
	ctx context.Context,
	opts *Options,
	store db.Store,
	client *http.Client,
	city *config.CityConfig,
	src boundarySource,
	fetch boundaryFetchers,
) (string, *Boundary, error) {
	unstrippedSource := src.name
	fmt.Fprintf(opts.IO.ErrOut, "Fetching boundary for %s from %s...\n", city.Name, src.from)
	unstripped, err := src.fetch(ctx, client, city.Name, fetch)
	if err != nil {
		// A canceled fetch needs no remediation, as for the water strip below.
		if errors.Is(err, context.Canceled) {
			return "", nil, fmt.Errorf("fetch boundary: %w", err)
		}
		return "", nil, hint.Withf(fmt.Errorf("fetch boundary: %w", err),
			"%s", boundaryFetchHint(err))
	}

	bbox, err := geo.BBoxFromGeoJSON(unstripped)
	if err != nil {
		// Nothing can be fetched over it: save it unstripped, as before
		// roads were fetched here, and let the roads step fail on it.
		fmt.Fprintf(opts.IO.ErrOut, "  water strip skipped: bbox: %v\n", err)
		if err := store.SaveBoundary(ctx, unstripped, unstrippedSource); err != nil {
			return "", nil, fmt.Errorf("save boundary: %w", err)
		}
		fmt.Fprintf(opts.IO.ErrOut, "  Boundary saved (source=%s).\n", unstrippedSource)
		return unstripped, &Boundary{}, nil
	}

	fmt.Fprintf(opts.IO.ErrOut, "  Fetching roads over the unstripped boundary as land evidence for the water strip...\n")
	roads, err := fetch.roads(ctx, client, bbox)
	if err == nil && ctx.Err() == nil && city.Overpass &&
		slices.Contains(roads.sources, resource.SourceOverpass) && !slices.Contains(roads.succeeded, resource.SourceOverpass) {
		// The OSM roads are the land evidence of a city that uses Overpass
		// (landEvidenceRoads); without them the strip would fall back to
		// the interior points, and the weaker boundary would be stored for
		// good. So, as when every source fails, nothing is saved and the
		// next ingest retries, even though another source answered.
		err = fmt.Errorf("roads: %s failed, and its roads are the water strip's land evidence", resource.SourceOverpass)
		roads.attempts = failAnswered(roads.attempts, fmt.Errorf("not saved: %w", err))
	}
	if err != nil {
		recordAttempts(ctx, opts, store, roads.attempts)
		if cerr := ctx.Err(); cerr != nil {
			// A canceled source counts as failed in fetchFromSources; the
			// user stopped the run, so no retry advice.
			return "", nil, fmt.Errorf("water strip for %s: roads: %w", city.Name, cerr)
		}
		err = fmt.Errorf("water strip for %s: %w: %w", city.Name, ErrRoadsFetch, err)
		return "", nil, hint.Withf(err,
			"re-run in a few minutes: the boundary was not saved, so the next ingest retries the roads fetch; "+
				"the warnings above name the road source that failed, and Overpass is usually just overloaded")
	}

	// From here on a failure saves neither the boundary nor the roads, so
	// the sources that answered are recorded as failed, as a roads ingest
	// whose save failed records them (failAnswered).
	// A canceled run records nothing, as a canceled request leaves no row.
	unsaved := func(err error) (string, *Boundary, error) {
		if !errors.Is(err, context.Canceled) && ctx.Err() == nil {
			recordAttempts(ctx, opts, store, failAnswered(roads.attempts, fmt.Errorf("not saved: the boundary failed: %w", err)))
		}
		return "", nil, err
	}
	stripped, err := stripWithLandEvidence(ctx, opts, client, city, fetch.water, unstripped, roads.features)
	if err != nil {
		return unsaved(err)
	}
	savedSource := unstrippedSource
	if stripped != "" {
		savedSource += "+osm-water"
	}

	final, finalSource, note, err := chooseBoundary(ctx, city.Name, unstripped, stripped, savedSource, unstrippedSource, roads.features)
	if err != nil {
		return unsaved(err)
	}
	if note != "" {
		cmdutil.Warnf(opts.IO, "%s", note)
	}
	if err := store.SaveBoundary(ctx, final, finalSource); err != nil {
		return unsaved(fmt.Errorf("save boundary: %w", err))
	}
	fmt.Fprintf(opts.IO.ErrOut, "  Boundary saved (source=%s).\n", finalSource)
	return final, &Boundary{roads: saveBoundaryRoads(ctx, opts, store, city, final, roads)}, nil
}

// stripWithLandEvidence strips water from unstripped with the city's roads
// as land probes, falling back to the boundary's interior points when no
// road qualifies. It returns the stripped boundary, or "" when nothing was
// stripped (no water, or a soft failure, which it warns about); err is a
// HARD failure (stripWaterFromBoundary) with its hint attached.
func stripWithLandEvidence(
	ctx context.Context,
	opts *Options,
	client *http.Client,
	city *config.CityConfig,
	fetchWater waterFetcher,
	unstripped string,
	roads []db.Feature,
) (string, error) {
	probes := ingestpkg.RoadLandProbes(landEvidenceRoads(roads, city), unstripped)
	if len(probes.Points) == 0 {
		cmdutil.Warnf(opts.IO, "water strip: no road probes for %s (fewer than %d roads inside its boundary), using the boundary's interior points",
			city.Name, ingestpkg.RoadProbeMinCells)
		var err error
		if probes, err = ingestpkg.InteriorLandProbes(unstripped); err != nil {
			fmt.Fprintf(opts.IO.ErrOut, "  water strip skipped: interior points: %v\n", err)
			return "", nil
		}
	}

	fmt.Fprintf(opts.IO.ErrOut, "  Fetching water features from Overpass to strip from the boundary...\n")
	stripped, warn, stripErr := stripWaterFromBoundary(ctx, client, fetchWater, unstripped, probes)
	if errors.Is(stripErr, context.Canceled) {
		// The user stopped it; no "Overpass is overloaded" advice.
		return "", fmt.Errorf("water strip for %s: %w", city.Name, stripErr)
	}
	if errors.Is(stripErr, ErrWaterFetch) {
		return "", hint.Withf(fmt.Errorf("water strip for %s: %w", city.Name, stripErr),
			"re-run in a few minutes: the boundary was not saved, so the next ingest retries the water fetch, "+
				"and Overpass is usually just overloaded")
	}
	if stripErr != nil {
		return "", hint.Withf(fmt.Errorf("water strip for %s: %w", city.Name, stripErr),
			"re-run with -vv and open an issue with the city name, bbox and the 'water way:', "+
				"'water relation:' or 'water coastline:' debug lines, which name the offending OSM way "+
				"or relation id: the OSM water-stitching pipeline usually produced a polygon covering "+
				"more than the city boundary")
	}
	if warn != "" {
		fmt.Fprintf(opts.IO.ErrOut, "  %s\n", warn)
	}
	return stripped, nil
}

// landEvidenceRoads is the roads whose points are land probes: the OSM
// (Overpass) roads when the city has Overpass enabled, and the ArcGIS roads
// only when ArcGIS is its sole road source. ArcGIS centerlines carry no
// bridge or tunnel tags, so beside OSM they would put a probe on every
// bridge OSM's tags leave out, and a river with a few bridges would read as
// city land. Every source's roads are still saved.
func landEvidenceRoads(roads []db.Feature, city *config.CityConfig) []db.Feature {
	if !city.Overpass {
		return roads
	}
	var osm []db.Feature
	for _, r := range roads {
		if r.SourceAPI == resource.SourceOverpass {
			osm = append(osm, r)
		}
	}
	return osm
}

// saveBoundaryRoads saves the roads fetched over the unstripped boundary's
// bbox that touch the saved boundary's bbox (ingest.FeaturesTouchingBBox):
// the rows a fetch over that bbox would store, so feature counts, and the
// routing-roads ring that excludes the boundary bbox
// (resource.ExcludingQuerier), are those of a plain roads ingest. Each
// source's attempt records the count it saved. Nothing fetched leaves the
// stored roads untouched, as fetchAndSave does.
func saveBoundaryRoads(ctx context.Context, opts *Options, store db.Store, city *config.CityConfig, boundary string, fetched roadsFetch) *roadsOutcome {
	out := &roadsOutcome{succeeded: fetched.succeeded}
	for _, name := range fetched.sources {
		if !slices.Contains(fetched.succeeded, name) {
			out.failed = append(out.failed, name)
		}
	}
	features := fetched.features
	if bbox, err := geo.BBoxFromGeoJSON(boundary); err == nil {
		features = ingestpkg.FeaturesTouchingBBox(features, bbox)
	}
	saved := map[string]int{}
	for _, f := range features {
		saved[f.SourceAPI]++
	}
	attempts := slices.Clone(fetched.attempts)
	for i, a := range attempts {
		if a.Outcome == db.IngestFailed {
			continue
		}
		attempts[i].FeatureCount, attempts[i].Outcome = saved[a.Source], db.IngestOK
		if saved[a.Source] == 0 {
			attempts[i].Outcome = db.IngestEmpty
		}
	}
	if len(features) == 0 {
		recordAttempts(ctx, opts, store, attempts)
		logs.From(ctx).Debug("no features fetched", "resource", resource.TypeRoads)
		return out
	}
	logs.From(ctx).Info("saving features to database", "count", len(features), "resource", resource.TypeRoads)
	replace := replaceScope(cmdutil.SourceAll, len(fetched.sources), fetched.succeeded)
	if err := store.UpsertFeatures(ctx, resource.TypeRoads, features, replace); err != nil {
		out.err = fmt.Errorf("save features: %w", err)
		recordAttempts(ctx, opts, store, failAnswered(attempts, out.err))
		return out
	}
	recordAttempts(ctx, opts, store, attempts)
	out.saved = len(features)
	fmt.Fprintf(opts.IO.ErrOut, "  Saved %d roads for %s from %s with the boundary; the roads step reuses them.\n",
		out.saved, city.Name, strings.Join(fetched.succeeded, ", "))
	return out
}

// boundaryFetchHint picks a remediation message tailored to the failure
// mode. The Nominatim-only "set boundary_relation_id" hint is misleading
// when the failure came from the relation path itself.
func boundaryFetchHint(err error) string {
	switch {
	case errors.Is(err, ingestpkg.ErrBoundaryRelationNotFound):
		return "OSM relation not found or has no closed outer rings. Verify the " +
			"relation id is an admin_level=8 boundary at https://overpass-turbo.eu/ " +
			`with: relation(<id>);out;`
	case errors.Is(err, ingestpkg.ErrBoundaryRelationTooLarge):
		return "OSM relation spans >5° — usually a county or state relation id by " +
			"mistake. Find the correct admin_level=8 city relation at " +
			"https://overpass-turbo.eu/."
	default:
		return "If Nominatim returned a non-Polygon result, set [[cities]].boundary_relation_id " +
			"to the OSM admin_level=8 relation for this city. Find it with: " +
			"https://overpass-turbo.eu/ → " +
			`relation["name"="<city>"]["boundary"="administrative"]["admin_level"="8"];out;`
	}
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

// ErrWaterFetch signals that the OSM water fetch itself failed (network
// error, upstream timeout, a response still truncated at the deepest
// quadrant split). It is a HARD failure, unlike the geometry-side soft
// failures: falling back would save the UNSTRIPPED boundary, and every
// later ingest reuses a saved boundary ("Using cached boundary"), so one
// transient Overpass hiccup would permanently leave open water in the
// city's area denominator. Failing saves nothing,
// so the next ingest retries.
var ErrWaterFetch = errors.New("water fetch failed")

// ErrRoadsFetch signals that every road source failed while resolving a
// fresh boundary. The roads are the water strip's land evidence, so it is a
// HARD failure like ErrWaterFetch: nothing is saved, and the next ingest
// retries.
var ErrRoadsFetch = errors.New("roads fetch failed")

// waterFetcher abstracts the OSM water fetch so tests can inject a
// fake response without spinning up an httptest server inside the
// internal/ingest package. The interface is intentionally narrow and is
// defined in the consumer, not in internal/ingest.
// probes are points on the city's land (ingestpkg.LandProbes); the
// water-stitching pipeline uses them to drop water candidates that hold
// city land and to disambiguate coastline closures.
type waterFetcher func(ctx context.Context, client *http.Client, bbox [4]float64, probes ingestpkg.LandProbes) (string, error)

// stripWaterFromBoundary tries to subtract OSM water from boundaryGJSON,
// with probes as the city's land (see ingest.FetchOSMWater).
//
// Returns (stripped, warn, err):
//   - (gjson, "", nil)  - success; use stripped boundary, caller appends
//     "+osm-water" to its existing source label
//   - ("", "", nil)     - no water in bbox; use unstripped boundary
//   - ("", warn, nil)   - soft failure (geometry: bbox, subtract); log warn
//     and fall back. These are deterministic, so a retry could not succeed
//     where this run failed.
//   - ("", "", err)     - HARD failure: the water fetch failed
//     (ErrWaterFetch; retryable, so nothing may be saved) or the
//     over-subtraction guard tripped (ErrWaterStripOverSubtracted); abort
//
// boundaryGJSON itself is preserved verbatim: the caller holds onto it for
// the road-coverage backstop (chooseBoundary), whose fallbacks are built
// from it.
//
// The err return wraps ErrWaterFetch or ErrWaterStripOverSubtracted (the
// latter with areas + ratio); callers should attach the city name via
// ErrHint so the operator can identify which city is affected.
//
// The source label is composed by the caller (fetchBoundary) so the
// upstream fetch (nominatim vs overpass-relation) is preserved in the
// final label — e.g., "nominatim+osm-water" or "overpass-relation+osm-water".
func stripWaterFromBoundary(
	ctx context.Context,
	client *http.Client,
	fetchWater waterFetcher,
	boundaryGJSON string,
	probes ingestpkg.LandProbes,
) (gjson, warn string, err error) {
	bbox, err := geo.BBoxFromGeoJSON(boundaryGJSON)
	if err != nil {
		return "", fmt.Sprintf("water strip skipped: bbox: %v", err), nil
	}
	waterGJSON, err := fetchWater(ctx, client, bbox, probes)
	if err != nil {
		return "", "", fmt.Errorf("%w: %w", ErrWaterFetch, err)
	}
	if waterGJSON == "" {
		return "", "", nil
	}
	stripped, err := geo.SubtractGeoJSON(ctx, boundaryGJSON, waterGJSON)
	if err != nil {
		return "", fmt.Sprintf("water strip skipped: subtract: %v", err), nil
	}
	origArea, errOrig := geo.BoundaryArea(ctx, boundaryGJSON)
	stripArea, errStrip := geo.BoundaryArea(ctx, stripped)
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

func resolveSources(opts *Options, bbox [4]float64, exclude *[4]float64, overpass bool, arcgisURL string, allowPrivateArcGIS bool, ios *iostreams.IOStreams) ([]ingestpkg.Source, error) {
	srcOpts := ingestpkg.Options{Progress: ios.ErrOut, AllowPrivateArcGIS: allowPrivateArcGIS, OverpassExclude: exclude, OverpassURL: opts.overpassURL}
	// An own-sourced resource has exactly one source, fixed by the resource
	// rather than the city config (resource.OwnSourced). The --source flag
	// admits only "all" or that source's name (sourceValues), both of which
	// mean the one source.
	if _, ok := opts.ResourceType.(resource.OwnSourced); ok {
		return ingestpkg.SourcesFor(opts.ResourceType, bbox, overpass, arcgisURL, srcOpts), nil
	}
	if opts.Source == cmdutil.SourceAll {
		return ingestpkg.AllSources(bbox, overpass, arcgisURL, srcOpts), nil
	}
	// SourceByName builds AllSources and reports "unknown source" when nothing
	// matches, which is the wrong diagnosis for a source that IS a valid
	// --source value (sourceValues) but is switched off for this city.
	// The user typed nothing wrong; their pvmt.toml disagrees with their flag,
	// and "unknown source: overpass" sends them looking for a typo. The dry
	// run warns with the same disabledSourceReason.
	if disabled := disabledSourceReason(opts.Source, overpass, arcgisURL); disabled != "" {
		return nil, hint.Withf(
			fmt.Errorf("source %q is not available for this city", opts.Source),
			"%s", disabled)
	}
	src, err := ingestpkg.SourceByName(string(opts.Source), bbox, overpass, arcgisURL, srcOpts)
	if err != nil {
		return nil, fmt.Errorf("resolving source %q: %w", opts.Source, err)
	}
	return []ingestpkg.Source{src}, nil
}

// censusOptOut keeps a city with census = false out of every census step,
// `pvmt census ingest` and its --dry-run included, as `pvmt census compute`
// already does: a quiet skip, exit 3.
func censusOptOut(opts *Options, city *config.CityConfig) error {
	if opts.ResourceType.Type() != resource.TypeCensusBlocks || city.CensusEnabled() {
		return nil
	}
	fmt.Fprintf(opts.IO.ErrOut, "Skipping %s: census = false.\n", city.Name)
	return cmdutil.ErrNoResults
}

// checkSourceForCities refuses, as a usage error and before any work, a
// named --source that the config switches off for any selected city. Cities
// that do not resolve are left for the run to report.
func checkSourceForCities(f *cmdutil.Factory, opts *Options) error {
	if _, ok := opts.ResourceType.(resource.OwnSourced); ok || opts.Source == cmdutil.SourceAll {
		return nil
	}
	cities, err := cmdutil.ResolveCities(f)
	if err != nil {
		return nil //nolint:nilerr // the run reports an unresolved city itself
	}
	for i := range cities {
		if disabled := disabledSourceReason(opts.Source, cities[i].Overpass, cities[i].ArcGISURL); disabled != "" {
			return hint.Withf(cmdutil.FlagErrorf("--source %s is not available for %s", opts.Source, cities[i].Name), "%s", disabled)
		}
	}
	return nil
}

// checkSourceEnabled refuses a named --source the city's config switched off.
// resolveSources makes the same check; this runs it before any network work.
// Own-sourced resources ignore the city's source settings, so never fail here.
func checkSourceEnabled(opts *Options, city *config.CityConfig) error {
	if _, ok := opts.ResourceType.(resource.OwnSourced); ok || opts.Source == cmdutil.SourceAll {
		return nil
	}
	if disabled := disabledSourceReason(opts.Source, city.Overpass, city.ArcGISURL); disabled != "" {
		return hint.Withf(fmt.Errorf("--source %s is not available for %s", opts.Source, city.Name), "%s", disabled)
	}
	return nil
}

// disabledSourceReason explains why an otherwise-valid --source value yields no
// source for this city, or "" when the source is available. It mirrors
// ingest.AllSources' two conditions, which are the only two ways a named source
// can be missing.
func disabledSourceReason(source cmdutil.Source, overpass bool, arcgisURL string) string {
	switch {
	case source == cmdutil.SourceOverpass && !overpass:
		return "set overpass = true for this city in pvmt.toml, or pick another --source"
	case source == cmdutil.SourceArcGIS && arcgisURL == "":
		return "set arcgis_url for this city in pvmt.toml, or pick another --source"
	}
	return ""
}

// fetchFromSources fetches from each source, tolerating per-source failures as
// long as one succeeds. It returns the fetched features, the names of the
// sources that succeeded (which equal the source_api written on their rows), so
// the caller can scope persistence to the sources that actually returned, and
// how each source answered, for recordAttempts. A source whose request the
// user canceled has no attempt.
func fetchFromSources(ctx context.Context, sources []ingestpkg.Source, client *http.Client, opts *Options, cityName string) ([]db.Feature, []string, []db.IngestAttempt, error) {
	var allFeatures []db.Feature
	var succeeded []string
	var attempts []db.IngestAttempt
	failures := 0
	input := string(opts.ResourceType.Type())
	for _, src := range sources {
		serves := true
		if ts, ok := src.(ingestpkg.TypeServer); ok {
			serves = ts.Serves(opts.ResourceType)
		}
		if serves {
			fmt.Fprintf(opts.IO.ErrOut, "Fetching %s from %s for %s...\n", opts.ResourceType.Type(), src.Name(), cityName)
		} else {
			fmt.Fprintf(opts.IO.ErrOut, "Skipping %s for %s: it serves only %s\n", src.Name(), opts.ResourceType.Type(), servedTypes(src))
		}
		started := time.Now()
		features, err := src.Fetch(ctx, client, opts.ResourceType)
		attempt := db.IngestAttempt{Input: input, Source: src.Name(), StartedAt: started, FinishedAt: time.Now()}
		if err != nil {
			cmdutil.Warnf(opts.IO, "%s fetch failed: %v", src.Name(), err)
			failures++
			if !errors.Is(err, context.Canceled) {
				attempt.Outcome, attempt.Error = db.IngestFailed, err.Error()
				attempts = append(attempts, attempt)
			}
			continue
		}
		if serves {
			fmt.Fprintf(opts.IO.ErrOut, "  Got %d features from %s\n", len(features), src.Name())
		}
		attempt.Outcome, attempt.FeatureCount = db.IngestOK, len(features)
		if len(features) == 0 {
			attempt.Outcome = db.IngestEmpty
		}
		attempts = append(attempts, attempt)
		allFeatures = append(allFeatures, features...)
		succeeded = append(succeeded, src.Name())
	}
	if len(sources) > 0 && failures == len(sources) {
		return nil, nil, attempts, fmt.Errorf("%s: %w", opts.ResourceType.Type(), cmdutil.ErrAllSourcesFailed)
	}
	return allFeatures, succeeded, attempts, nil
}

// failAnswered marks every source that answered as failed with err: its rows
// were fetched but not saved.
func failAnswered(attempts []db.IngestAttempt, err error) []db.IngestAttempt {
	out := slices.Clone(attempts)
	for i := range out {
		if out[i].Outcome != db.IngestFailed {
			out[i].Outcome, out[i].FeatureCount, out[i].Error = db.IngestFailed, 0, err.Error()
		}
	}
	return out
}

// recordAttempts stores attempts (ingest_attempts), best-effort: a failure to
// record is warned about and never fails the ingest. It records even when ctx
// is canceled, so the sources that answered before a Ctrl-C keep their trace.
func recordAttempts(ctx context.Context, opts *Options, store db.Store, attempts []db.IngestAttempt) {
	if err := store.RecordIngestAttempts(context.WithoutCancel(ctx), attempts); err != nil {
		cmdutil.Warnf(opts.IO, "could not record the ingest attempt: %v", err)
	}
}

// withRetryNotices makes every request under ctx print a line to ios.ErrOut
// when the HTTP client waits to retry it, so minutes of backoff against a busy
// Overpass are not silent.
func withRetryNotices(ctx context.Context, ios *iostreams.IOStreams) context.Context {
	return ingestpkg.WithRetryNotifier(ctx, func(n ingestpkg.RetryNotice) {
		wait := n.Wait.Round(time.Second)
		if n.Wait < 10*time.Second {
			wait = n.Wait.Round(100 * time.Millisecond) // a jittered first retry is under a second
		}
		fmt.Fprintf(ios.ErrOut, "  %s busy (%s), retrying in %s (retry %d of %d)\n",
			n.Host, n.Reason, wait, n.Attempt, n.Retries)
	})
}
