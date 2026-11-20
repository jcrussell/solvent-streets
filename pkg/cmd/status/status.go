package status

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jcrussell/solvent-streets/internal/coherence"
	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/internal/geo"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
	"github.com/jcrussell/solvent-streets/internal/services"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

type Options struct {
	IO     *iostreams.IOStreams
	CityDB func() (db.Store, error)
	// Config and CurrentCity resolve the city's snapshot key, so status can
	// say when the city's snapshots were computed under other settings. Either
	// may be nil (no config): status then reports what it can without it.
	Config       func() (*config.Config, error)
	CurrentCity  func() (*config.CityConfig, error)
	UnitSystem   func() units.System
	ResourceType resource.Source // nil for global status
	Exporter     cmdutil.Exporter
	rows         *[]statusRow // multi-city accumulator; nil for single-pass JSON

	// All reports every configured city (or the one --city names) as one
	// board, a row per layer. Incomplete and Stale
	// keep only the rows needing an ingest, or also a compute; ExitStatus
	// exits 1 when any row does. BoardCities lists the cities with their
	// stores.
	All, Incomplete, Stale, ExitStatus bool
	BoardCities                        func(ctx context.Context) ([]boardCity, error)
}

type statusRow struct {
	// City is the city's slug; empty (and omitted) without a config.
	City string `json:"city,omitempty"`
	// ResourceType is a pavement row's resource; Layer, the public name
	// (resource.Type.PublicName) of any other stored input (`services
	// status`, `census status`). A row has exactly one of them.
	ResourceType  string  `json:"resource_type,omitempty"`
	Layer         string  `json:"layer,omitempty"`
	FeatureCount  int     `json:"feature_count"`
	LastIngestAt  string  `json:"last_ingest_at,omitempty"`
	LastComputeAt string  `json:"last_compute_at,omitempty"`
	Area          float64 `json:"area,omitempty"`
	// EstimatedSpaces is the parking resource's estimated vehicle capacity
	// from its last compute; 0 and omitted for other resources.
	EstimatedSpaces int `json:"estimated_spaces,omitempty"`
	// State is the resource's part of the latest compute: "current",
	// "stale" (StaleInputs names the inputs ingested again since) or
	// "not_computed" (coherence.PartStates). Omitted without a config.
	State       string   `json:"state,omitempty"`
	StaleInputs []string `json:"stale_inputs,omitempty"`
	stateText   string   // the table cell, coherence.PartState.String
}

var _ cmdutil.RowExporter = statusRow{}

func (r statusRow) ExportData(fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		if v, ok := r.field(f); ok {
			out[f] = v
		}
	}
	return out
}

// field is one --json field's value; ok is false for a field that does not
// apply to the row (omitted rather than emitted as "" or a 0 sentinel).
func (r statusRow) field(f string) (any, bool) {
	switch f {
	case "city":
		return r.City, r.City != ""
	case "resource_type":
		return r.ResourceType, r.ResourceType != ""
	case fieldLayer:
		return r.Layer, r.Layer != ""
	case "feature_count":
		return r.FeatureCount, true
	case "last_ingest_at":
		return r.LastIngestAt, r.LastIngestAt != ""
	case "last_compute_at":
		return r.LastComputeAt, r.LastComputeAt != ""
	case "area":
		// Area is a compute result: a computed 0 is a value, but a
		// never-computed row has none.
		return r.Area, r.LastComputeAt != ""
	case "estimated_spaces":
		return r.EstimatedSpaces, r.EstimatedSpaces != 0
	case "state":
		return r.State, r.State != ""
	case "stale_inputs":
		return r.StaleInputs, len(r.StaleInputs) > 0
	}
	return nil, false
}

// Short is the one-line help of the status subcommand of a command group
// whose features are noun.
func Short(noun string) string {
	return "Show ingest and compute status for " + noun
}

// paved reports whether rt is a pavement resource, the only kind with an
// area and estimated spaces.
func paved(rt resource.Source) bool {
	t := rt.Type()
	return !t.IsLayer() && !t.IsFacility()
}

// Long is the --help description of the status subcommand of a command group
// whose features are noun. Only a paved resource reports an area.
func Long(noun string, rt resource.Source) string {
	area := ""
	if paved(rt) {
		area = ", and paved area in the active unit system"
	}
	return cmdutil.Reflow(fmt.Sprintf(`Report ingest and compute progress for %s in every configured city,
or the one --city names, one row per city: ingested feature count
(everything the bounding-box fetch returned, before the boundary clip),
last-ingest and last-compute timestamps%s, from the city's latest compute,
and the state of its part: current, stale (an input it read was ingested
again since) or not computed.`, noun, area), cmdutil.HelpWidth)
}

// Example is the --help example text for the status subcommand of the
// command group named group (`pvmt <group> status`). The group name is not
// derivable from the resource type (census blocks are stored as
// "layer-census-blocks" but their command is 'pvmt census').
func Example(group string, rt resource.Source) string {
	fields := "city,layer,feature_count,state"
	if paved(rt) {
		fields = "city,resource_type,feature_count,area"
	}
	return fmt.Sprintf(`  # Show feature and result counts for every configured city
  pvmt %s status

  # Emit the rows as JSON
  pvmt %s status --json %s`, group, group, fields)
}

// fieldLayer is the --json field naming a row's stored input.
const fieldLayer = "layer"

var statusFields = []string{"city", "resource_type", "feature_count", "last_ingest_at", "last_compute_at", "area", "estimated_spaces", "state", "stale_inputs"}

// fieldsFor is the --json fields of a status over rt (nil: every
// resource). Only pavement has a resource_type, an area and estimated
// spaces; any other input is named as a layer.
func fieldsFor(rt resource.Source) []string {
	if rt == nil || paved(rt) {
		return statusFields
	}
	out := slices.DeleteFunc(slices.Clone(statusFields), func(f string) bool {
		return f == "area" || f == "estimated_spaces"
	})
	out[slices.Index(out, "resource_type")] = fieldLayer
	return out
}

func NewCmdStatus(f *cmdutil.Factory, rt resource.Source, runF func(context.Context, *Options) error) *cobra.Command {
	opts := &Options{
		IO:           f.IOStreams,
		CityDB:       f.CityDB,
		Config:       f.Config,
		CurrentCity:  f.CurrentCity,
		UnitSystem:   f.UnitSystem,
		ResourceType: rt,
	}

	use := "status"
	short := "Show overall status"
	long := `Report ingest and compute progress across every resource type for
every configured city, or the one --city names, from each city's latest
compute: ingested feature counts (everything the bounding-box fetch
returned, before the boundary clip), last-ingest and last-compute
timestamps, paved area totals in the active unit system, and each part's
state: current, stale (an input it read was ingested again since) or not
computed. Each city's table is followed by the combined pass, service
coverage and census population, then how many parts of the latest compute
are not yet snapshotted. On a TTY each city ends with a city-area summary
(paved-vs-total %, footnoted when the combined total is stale or missing)
and its five most recent snapshots with their config hash, marking the
published (newest) one and those created under the current settings. When
nothing is computed under the current settings, a hint says why and how to
recompute. --json emits one row per city and resource.`
	example := `  # Show feature and result counts across every resource and city
  pvmt status

  # One city
  pvmt --city oakland-ca status

  # Emit one row per city and resource as JSON
  pvmt status --json city,resource_type,feature_count,area`
	if rt != nil {
		short, long = Short(string(rt.Type())), Long(string(rt.Type()), rt)
		example = Example(string(rt.Type()), rt)
	}

	fields := fieldsFor(rt)
	if rt == nil {
		long += boardHelp
		example += boardExample
		// The board shares some field names with the rows (city,
		// feature_count, state); list each once.
		fields = slices.Clone(statusFields)
		for _, bf := range boardFields {
			if !slices.Contains(fields, bf) {
				fields = append(fields, bf)
			}
		}
		opts.BoardCities = func(ctx context.Context) ([]boardCity, error) { return boardCities(ctx, f) }
	}

	cmd := &cobra.Command{
		Use:     use,
		Args:    cobra.NoArgs,
		Short:   short,
		Long:    long,
		Example: example,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !opts.All && (opts.Incomplete || opts.Stale || opts.ExitStatus) {
				return cmdutil.FlagErrorf("--incomplete, --stale and --exit-status need --all")
			}
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			if opts.All {
				return runBoard(cmd.Context(), opts)
			}
			return runStatusAllCities(cmd.Context(), f, opts)
		},
	}

	cmdutil.AddJSONFlags(cmd, &opts.Exporter, fields)
	if rt == nil {
		cmd.Flags().BoolVar(&opts.All, "all", false, "Report every configured city as one board, a row per layer")
		cmd.Flags().BoolVar(&opts.Incomplete, "incomplete", false, "With --all, show only layers missing data or with a failing source")
		cmd.Flags().BoolVar(&opts.Stale, "stale", false, "With --all, also show layers whose part is stale or not computed")
		cmd.Flags().BoolVar(&opts.ExitStatus, "exit-status", false, "With --all, exit 1 when any layer is incomplete (or, with --stale, not current)")
	}

	return cmd
}

// resolveCityFirst resolves the config and city before the database opens, as
// the other commands do, so a missing pvmt.toml or an unknown --city says so
// instead of reading as a database error. Either may be
// nil (no config).
func resolveCityFirst(opts *Options) error {
	if opts.Config != nil {
		if _, err := opts.Config(); err != nil {
			return err
		}
	}
	if opts.CurrentCity != nil {
		if _, err := opts.CurrentCity(); err != nil {
			return err
		}
	}
	return nil
}

// runStatusAllCities reports every configured city, or the one --city
// names, with --json rows for all of them written as one document. Without
// a config, or with no cities in it, it reports through the current city,
// which says what is missing.
func runStatusAllCities(ctx context.Context, f *cmdutil.Factory, opts *Options) error {
	if f.Config == nil {
		return runStatus(ctx, opts)
	}
	if cfg, err := f.Config(); err != nil || cfg == nil || len(cfg.Cities) == 0 {
		return runStatus(ctx, opts)
	}
	return cmdutil.ForEachCityRows(ctx, f, opts.IO, opts.Exporter, func(ctx context.Context, cf *cmdutil.Factory, rows *[]statusRow) error {
		cityOpts := *opts
		cityOpts.CurrentCity, cityOpts.CityDB, cityOpts.rows = cf.CurrentCity, cf.CityDB, rows
		return runStatus(ctx, &cityOpts)
	})
}

func runStatus(ctx context.Context, opts *Options) error {
	ios := opts.IO

	if err := resolveCityFirst(opts); err != nil {
		return err
	}
	store, err := opts.CityDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	var sources []resource.Source
	if opts.ResourceType != nil {
		sources = []resource.Source{opts.ResourceType}
	} else {
		sources = resource.All
	}

	sys := opts.UnitSystem()
	view := opts.loadLatest(ctx, store)

	rows, err := buildRows(ctx, ios, store, sources, view)
	if err != nil {
		return err
	}
	// JSON output
	if opts.rows != nil {
		*opts.rows = append(*opts.rows, rows...)
		return nil
	}
	if opts.Exporter != nil {
		return cmdutil.WriteRows(ios, opts.Exporter, rows)
	}

	if err := printTable(ios, rows, sys, view.ok); err != nil {
		return err
	}

	allResources := opts.ResourceType == nil
	if allResources {
		view.printOtherParts(ios)
	}
	view.printUnsnapshotted(ios)
	view.noteStale(ios)

	if ios.IsTTY() {
		printCitySummary(ctx, ios, store, rows, sys, allResources, view)
		if view.snapsErr == nil {
			printSnapshotHistory(ios, view.snaps, view.hash, view.slug())
		}
	}

	return nil
}

// buildRows reads each source's row: its ingest stats and compute totals
// from store and, with a view, its part's state. A failed read of the compute
// totals fails the status, since rows without them would read as never
// computed.
func buildRows(ctx context.Context, ios *iostreams.IOStreams, store db.Store, sources []resource.Source, view latestView) ([]statusRow, error) {
	types := make([]resource.Type, len(sources))
	for i, rt := range sources {
		types[i] = rt.Type()
	}
	// One read of every source's totals. A type never computed is absent and
	// leaves its compute columns empty.
	totals, err := results.LoadTotals(ctx, store, types)
	if err != nil {
		return nil, fmt.Errorf("compute results: %w", err)
	}
	var rows []statusRow
	for _, rt := range sources {
		rtVal := rt.Type()
		info, err := store.Stats(ctx, rtVal)
		if err != nil {
			cmdutil.Warnf(ios, "could not get stats for %s: %v", rt.Type(), err)
			continue
		}
		row := statusRow{City: view.slug(), FeatureCount: info.FeatureCount}
		if paved(rt) {
			row.ResourceType = string(rtVal)
		} else {
			row.Layer = rtVal.PublicName()
		}
		if info.LastIngestAt != nil {
			row.LastIngestAt = info.LastIngestAt.UTC().Format(time.RFC3339)
		}
		if tot, ok := totals[rtVal]; ok {
			row.Area, row.EstimatedSpaces = tot.Area, tot.Spaces
			row.LastComputeAt = tot.ComputedAt.UTC().Format(time.RFC3339)
		}
		if st, ok := view.state(partOf(rtVal)); ok {
			row.State, row.stateText = st.State, st.String()
			for _, in := range st.StaleInputs {
				row.StaleInputs = append(row.StaleInputs, resource.Type(in).PublicName())
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// partOf is the latest-compute part computed from t's features: the
// service's coverage for its facilities, census for the census blocks, else
// the resource's own.
func partOf(t resource.Type) string {
	switch {
	case t.IsFacility():
		return db.ServiceProducer(t.FacilityService())
	case t == resource.TypeCensusBlocks:
		return db.ProducerCensus
	}
	return string(t)
}

// printTable renders rows, with a State column when withState.
func printTable(ios *iostreams.IOStreams, rows []statusRow, sys units.System, withState bool) error {
	tp := iostreams.NewTablePrinter(ios)
	kind := "Resource"
	if len(rows) > 0 && rows[0].Layer != "" {
		kind = "Layer"
	}
	header := []string{"City", kind, "Features", "Last Ingest", "Last Compute", units.AreaLabel(sys), units.AreaLargeLabel(sys), "Est. Spaces"}
	if withState {
		header = append(header, "State")
	}
	tp.AddHeader(header...)
	for _, r := range rows {
		spacesStr := "-"
		if r.EstimatedSpaces > 0 {
			spacesStr = strconv.Itoa(r.EstimatedSpaces)
		}
		cells := []string{
			r.City,
			r.ResourceType + r.Layer,
			strconv.Itoa(r.FeatureCount),
			iostreams.FormatTimestamp(r.LastIngestAt, ios.IsTTY()),
			iostreams.FormatTimestamp(r.LastComputeAt, ios.IsTTY()),
			fmt.Sprintf("%.0f", units.AreaValue(r.Area, sys)),
			fmt.Sprintf("%.1f", units.AreaLargeValue(r.Area, sys)),
			spacesStr,
		}
		if withState {
			cells = append(cells, r.stateText)
		}
		tp.AddRow(cells...)
	}
	return tp.Render()
}

// latestView is what status knows about the city's latest compute: its
// parts and their states under the current settings, and the city's
// snapshots. ok is false without a config (nothing is marked or hinted then).
type latestView struct {
	ok       bool
	cfg      *config.Config
	city     *config.CityConfig
	hash     string
	latest   []db.LatestPart
	inputs   []db.InputState
	snaps    []db.SnapshotCity
	snapsErr error
}

// loadLatest reads the city's latest compute, current inputs and snapshots.
// A read error leaves the view without states (ok false) rather than failing
// the status.
func (opts *Options) loadLatest(ctx context.Context, store db.Store) latestView {
	v := latestView{}
	v.snaps, v.snapsErr = store.ListSnapshots(ctx)
	v.cfg, v.city = opts.currentCity()
	if v.cfg == nil {
		return v
	}
	v.hash = v.cfg.CityHash(v.city)
	latest, err := store.LatestCompute(ctx, v.hash)
	if err != nil {
		return v
	}
	inputs, err := store.InputStates(ctx)
	if err != nil {
		return v
	}
	v.ok, v.latest, v.inputs = true, latest, inputs
	return v
}

// slug is the city's slug, "" without a config.
func (v latestView) slug() string {
	if v.city == nil {
		return ""
	}
	return v.city.Slug()
}

// section is the header of one of the sections after the table, naming the
// city: they share stdout with the table, while ForEachCity's per-city
// header goes to stderr, so 'pvmt status > out.txt' still says whose
// section each is.
func section(slug, title string) string {
	if slug == "" {
		return "\n=== " + title + " ===\n"
	}
	return "\n=== " + slug + ": " + title + " ===\n"
}

// cityPrefix is "<slug>: " for a line after the table ("" without a city).
func cityPrefix(slug string) string {
	if slug == "" {
		return ""
	}
	return slug + ": "
}

// state is part's state in the latest compute; ok is false without a view.
func (v latestView) state(part string) (coherence.PartState, bool) {
	if !v.ok {
		return coherence.PartState{}, false
	}
	return coherence.PartStates([]string{part}, v.latest, v.inputs)[0], true
}

// otherParts names the parts beyond the pavement resources that status
// reports: the combined pass, each service with a part or with facilities
// (ingested or configured), and census unless the city opts out.
func (v latestView) otherParts() []string {
	out := []string{string(resource.CombinedAll)}
	have := map[string]bool{}
	for _, p := range v.latest {
		have[p.Part] = true
	}
	stored := map[string]bool{}
	for _, st := range v.inputs {
		stored[st.Input] = true
	}
	for _, svc := range services.Names() {
		part := db.ServiceProducer(svc)
		configured := slices.ContainsFunc(v.city.Facilities, func(f config.FacilityConfig) bool { return f.Service == svc })
		if have[part] || configured || stored[string(resource.FacilityType(svc))] {
			out = append(out, part)
		}
	}
	if have[db.ProducerCensus] || v.city.CensusEnabled() {
		out = append(out, db.ProducerCensus)
	}
	return out
}

// printOtherParts lists the state of the combined pass, service coverage and
// census population.
func (v latestView) printOtherParts(ios *iostreams.IOStreams) {
	if !v.ok {
		return
	}
	fmt.Fprint(ios.Out, section(v.slug(), "Other parts"))
	for _, st := range coherence.PartStates(v.otherParts(), v.latest, v.inputs) {
		fmt.Fprintf(ios.Out, "  %-14s %s\n", st.Part, st)
	}
}

// printUnsnapshotted says how many parts of the latest compute the newest
// snapshot, the one export and serve publish, does not hold.
func (v latestView) printUnsnapshotted(ios *iostreams.IOStreams) {
	if !v.ok || v.snapsErr != nil || len(v.latest) == 0 {
		return
	}
	var newest *db.SnapshotCity
	if len(v.snaps) > 0 {
		newest = &v.snaps[0]
	}
	parts := db.UnsnapshottedParts(v.latest, newest)
	if len(parts) == 0 {
		return
	}
	noun := "part"
	if len(parts) > 1 {
		noun = "parts"
	}
	fmt.Fprintf(ios.Out, "\n%s%d %s not yet snapshotted (%s); run 'pvmt snapshots create <name>' to publish\n",
		cityPrefix(v.slug()), len(parts), noun, strings.Join(parts, ", "))
}

// noteStale prints the shared stale-settings hint when the city has
// snapshots but nothing computed under its current settings. "Last Compute"
// reads through the latest compute under the current settings, so such a
// city shows it blank; say why rather than leave it looking never-computed.
func (v latestView) noteStale(ios *iostreams.IOStreams) {
	if v.ok && v.snapsErr == nil && export.StaleSettings(v.cfg, v.city, v.snaps, v.latest) {
		fmt.Fprintf(ios.ErrOut, "hint: %s\n", v.cfg.StaleHint(v.city))
	}
}

// currentCity returns the config and the current city, or nils when either
// does not resolve (then nothing is marked or hinted).
func (opts *Options) currentCity() (*config.Config, *config.CityConfig) {
	if opts.Config == nil || opts.CurrentCity == nil {
		return nil, nil
	}
	cfg, err := opts.Config()
	if err != nil || cfg == nil {
		return nil, nil
	}
	city, err := opts.CurrentCity()
	if err != nil || city == nil {
		return nil, nil
	}
	return cfg, city
}

func printCitySummary(ctx context.Context, ios *iostreams.IOStreams, store db.Store, rows []statusRow, sys units.System, allResources bool, view latestView) {
	boundaryGJSON, err := store.GetBoundary(ctx)
	if err != nil || boundaryGJSON == "" {
		return
	}
	cityArea, err := geo.BoundaryArea(ctx, boundaryGJSON)
	if err != nil || cityArea <= 0 {
		return
	}
	totalPaved, accurate := totalPavedArea(ctx, store, rows, allResources)
	// A combined total computed from inputs ingested again since disagrees
	// with the rows above it: footnote it too.
	staleCombined := ""
	if st, ok := view.state(string(resource.CombinedAll)); allResources && accurate && ok && st.State == coherence.StateStale {
		staleCombined, accurate = strings.Join(st.Stale, ", "), false
	}
	fmt.Fprint(ios.Out, section(view.slug(), "City Summary"))
	fmt.Fprintf(ios.Out, "  City Area:    %s (%s)\n", units.FormatAreaLarge(cityArea, sys), units.FormatAreaVeryLarge(cityArea, sys))
	pavedLabel := "  Paved Area:   "
	if !accurate {
		// All-resources view with no combined (de-duplicated) compute row —
		// this is the per-resource sum, which double-counts road/parking/
		// sidewalk buffer overlap. Flag it so the number isn't mistaken for
		// the true union.
		pavedLabel = "  Paved Area*:  "
	}
	fmt.Fprintf(ios.Out, "%s%s (%s)\n", pavedLabel, units.FormatAreaLarge(totalPaved, sys), units.FormatAreaVeryLarge(totalPaved, sys))
	if totalPaved > 0 {
		if accurate {
			fmt.Fprintf(ios.Out, "  %% Paved:      %.1f%%\n", totalPaved/cityArea*100)
		} else {
			fmt.Fprintf(ios.Out, "  %% Paved*:     %.1f%%\n", totalPaved/cityArea*100)
		}
	}
	switch {
	case staleCombined != "":
		fmt.Fprintf(ios.Out, "  * the combined total is stale (%s since it was computed); run 'pvmt all compute' to recompute it.\n", staleCombined)
	case !accurate:
		fmt.Fprintf(ios.Out, "  * sum of per-resource areas (double-counts overlap); run 'pvmt all compute' for the de-duplicated total.\n")
	}
}

// totalPavedArea returns the paved area for the City Summary and whether that
// figure is free of cross-resource double-counting.
//
// An all-resources status reports the de-duplicated combined total
// (results.PavedArea, the figure meta.json publishes) and, until `pvmt all
// compute` has stored one, the sum of the rows in the table above it
// (accurate=false → footnoted).
//
// For a single-resource status (`pvmt <res> status`) the sum is just that one
// resource — no overlap to de-duplicate — so it is returned as-is and reported
// as accurate; pulling the combined union here would mismatch the single-row
// table above it.
func totalPavedArea(ctx context.Context, store db.Store, rows []statusRow, allResources bool) (float64, bool) {
	if allResources {
		if area, deduped, err := results.PavedArea(ctx, store, resource.ScopeAll); err == nil && deduped {
			return area, true
		}
	}
	var sum float64
	for _, r := range rows {
		sum += r.Area
	}
	// A single-resource sum has no cross-resource overlap, so it is accurate;
	// an all-resources sum reached here only as a fallback and double-counts.
	return sum, !allResources
}

// printSnapshotHistory lists the newest snapshots of the city slug with
// their config hash, marking the newest, which export and serve publish, and
// the ones created under the current settings (hash; "" marks none).
func printSnapshotHistory(ios *iostreams.IOStreams, snapshots []db.SnapshotCity, hash, slug string) {
	if len(snapshots) == 0 {
		return
	}
	fmt.Fprint(ios.Out, section(slug, "Snapshots"))
	limit := min(len(snapshots), 5)
	for i, s := range snapshots[:limit] {
		var marks []string
		if i == 0 {
			marks = append(marks, "published")
		}
		if hash != "" && s.ConfigHash == hash {
			marks = append(marks, "current settings")
		}
		mark := ""
		if len(marks) > 0 {
			mark = "  (" + strings.Join(marks, ", ") + ")"
		}
		fmt.Fprintf(ios.Out, "  %s  %s  (%s)  %d parts  %s%s\n", s.Name, s.CreatedAt.Format(time.RFC3339),
			iostreams.RelativeTime(s.CreatedAt), len(s.Parts), s.ConfigHash, mark)
	}
	if len(snapshots) > 5 {
		fmt.Fprintf(ios.Out, "  ... and %d more\n", len(snapshots)-5)
	}
}
