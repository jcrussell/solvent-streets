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
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/services"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// boardFields are the --json fields of `pvmt status --all`, one row per city
// and layer.
var boardFields = []string{"city", "layer", "feature_count", "last_success_at", "failing_sources", "error", "empty_upstream", "state", "incomplete", "retry"}

// boardRow is one city's layer on the status board: what is stored, how its
// sources last answered, and the state of the part computed from it.
type boardRow struct {
	City          string   `json:"city"`
	Layer         string   `json:"layer"`
	FeatureCount  int      `json:"feature_count"`
	LastSuccessAt string   `json:"last_success_at,omitempty"`
	Failing       []string `json:"failing_sources,omitempty"`
	Error         string   `json:"error,omitempty"`
	// EmptyUpstream marks a layer with no rows because every configured
	// source answered with none (a small town with no sidewalks mapped): not
	// a gap a retry can fill, so not incomplete.
	EmptyUpstream bool   `json:"empty_upstream,omitempty"`
	State         string `json:"state,omitempty"`
	Incomplete    bool   `json:"incomplete"`
	Retry         string `json:"retry,omitempty"`
	stateText     string
}

var _ cmdutil.RowExporter = boardRow{}

func (r boardRow) ExportData(fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		var v any
		ok := true
		switch f {
		case "city":
			v = r.City
		case "layer":
			v = r.Layer
		case "feature_count":
			v = r.FeatureCount
		case "last_success_at":
			v, ok = r.LastSuccessAt, r.LastSuccessAt != ""
		case "failing_sources":
			v, ok = r.Failing, len(r.Failing) > 0
		case "error":
			v, ok = r.Error, r.Error != ""
		case "empty_upstream":
			v = r.EmptyUpstream
		case "state":
			v, ok = r.State, r.State != ""
		case "incomplete":
			v = r.Incomplete
		case "retry":
			v, ok = r.Retry, r.Retry != ""
		default:
			ok = false
		}
		if ok {
			out[f] = v
		}
	}
	return out
}

// boardCity is one city the board reports on, with its store.
type boardCity struct {
	City  config.CityConfig
	Store db.Store
}

// boardLayer is one layer of a city: the input it is stored as, the
// sources it is fetched from, the command group that ingests it, the part
// computed from it ("" for none), and whether no rows at all makes it
// incomplete.
type boardLayer struct {
	name, input, group, part string
	sources                  []string
	needsRows                bool
}

// boardLayers lists city's layers: its boundary, each pavement resource from
// its configured sources, each service's facilities and routing roads, and
// census blocks unless the city opts out.
func boardLayers(city *config.CityConfig) []boardLayer {
	pavement := city.PavementSources()
	boundarySrc := "nominatim"
	if city.BoundaryRelationID > 0 {
		boundarySrc = "overpass-relation"
	}
	layers := []boardLayer{{name: db.InputBoundary, input: db.InputBoundary, group: "all", sources: []string{boundarySrc}, needsRows: true}}
	for _, rt := range resource.All {
		t := string(rt.Type())
		layers = append(layers, boardLayer{name: t, input: t, group: t, part: t, sources: pavement, needsRows: true})
	}
	for _, svc := range services.Names() {
		ft := string(resource.FacilityType(svc))
		// A service may have no OSM stations (all hand-placed), so an empty
		// fetch is not a gap; a failed one is.
		layers = append(layers,
			boardLayer{name: ft, input: ft, group: "services", part: db.ServiceProducer(svc), sources: []string{resource.SourceOverpass}},
			boardLayer{name: string(resource.TypeRoutingRoads), input: string(resource.TypeRoutingRoads), group: "services", sources: []string{resource.SourceOverpass}})
	}
	if city.CensusEnabled() {
		ct := string(resource.TypeCensusBlocks)
		layers = append(layers, boardLayer{name: ct, input: ct, group: "census", part: db.ProducerCensus, sources: []string{resource.SourceTigerweb}, needsRows: true})
	}
	return layers
}

// cityBoard builds one city's board rows. hash is the city's current config
// hash ("" without a config: no part states then).
func cityBoard(ctx context.Context, c boardCity, hash string) ([]boardRow, error) {
	history, err := c.Store.IngestHistory(ctx)
	if err != nil {
		return nil, fmt.Errorf("ingest history: %w", err)
	}
	layers := boardLayers(&c.City)
	states, err := partStates(ctx, c.Store, hash, layers)
	if err != nil {
		return nil, err
	}
	slug := c.City.Slug()
	rows := make([]boardRow, 0, len(layers))
	for _, l := range layers {
		count, err := layerRows(ctx, c.Store, l)
		if err != nil {
			return nil, err
		}
		row := boardRow{City: slug, Layer: l.name, FeatureCount: count}
		row.Failing = db.FailingSources(history, l.input, l.sources)
		row.LastSuccessAt, row.Error = lastAnswers(history, l.input, row.Failing)
		row.EmptyUpstream = count == 0 && len(row.Failing) == 0 && db.AnsweredEmpty(history, l.input, l.sources)
		row.Incomplete = len(row.Failing) > 0 || (l.needsRows && count == 0 && !row.EmptyUpstream)
		if st, ok := states[l.part]; ok {
			row.State, row.stateText = st.State, st.String()
		}
		switch {
		case row.Incomplete:
			row.Retry = retryIngest(slug, l, row.Failing)
		case row.State != "" && row.State != coherence.StateCurrent:
			row.Retry = fmt.Sprintf("pvmt --city %s all compute", slug)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// partStates returns the state of each layer's part in the latest compute
// under hash, keyed by part; empty when hash is "".
func partStates(ctx context.Context, store db.Store, hash string, layers []boardLayer) (map[string]coherence.PartState, error) {
	states := map[string]coherence.PartState{}
	if hash == "" {
		return states, nil
	}
	latest, err := store.LatestCompute(ctx, hash)
	if err != nil {
		return nil, err
	}
	inputs, err := store.InputStates(ctx)
	if err != nil {
		return nil, err
	}
	var parts []string
	for _, l := range layers {
		if l.part != "" {
			parts = append(parts, l.part)
		}
	}
	for _, st := range coherence.PartStates(parts, latest, inputs) {
		states[st.Part] = st
	}
	return states, nil
}

// layerRows is how many rows layer l has stored: its features, or 1 for a
// stored boundary.
func layerRows(ctx context.Context, store db.Store, l boardLayer) (int, error) {
	if l.input == db.InputBoundary {
		gj, err := store.GetBoundary(ctx)
		if err != nil || gj == "" {
			return 0, err
		}
		return 1, nil
	}
	info, err := store.Stats(ctx, resource.Type(l.input))
	if err != nil {
		return 0, err
	}
	return info.FeatureCount, nil
}

// lastAnswers returns input's newest success over all its sources, as an
// RFC 3339 time, and the newest error among failing.
func lastAnswers(history []db.IngestHistory, input string, failing []string) (lastSuccess, lastError string) {
	var success, failure *db.IngestAttempt
	for i := range history {
		h := &history[i]
		if h.Input != input {
			continue
		}
		if s := h.LastSuccess; s != nil && (success == nil || s.FinishedAt.After(success.FinishedAt)) {
			success = s
		}
		if f := h.LastFailure; f != nil && h.Failing() && slices.Contains(failing, h.Source) &&
			(failure == nil || f.FinishedAt.After(failure.FinishedAt)) {
			failure = f
		}
	}
	if success != nil {
		lastSuccess = success.FinishedAt.UTC().Format(time.RFC3339)
	}
	if failure != nil {
		lastError = failure.Source + ": " + failure.Error
	}
	return lastSuccess, lastError
}

// retryIngest is the command that re-fetches layer l of the city slug. A
// pavement layer with one failing source of two names it, so the other's
// rows are not fetched again.
func retryIngest(slug string, l boardLayer, failing []string) string {
	cmd := fmt.Sprintf("pvmt --city %s %s ingest", slug, l.group)
	if l.group == l.input && len(failing) == 1 && len(l.sources) > 1 {
		cmd += " --source " + failing[0]
	}
	return cmd
}

// filterBoard keeps the rows that need attention: incomplete ones, and with
// stale also those whose part is not current.
func filterBoard(rows []boardRow, stale bool) []boardRow {
	var out []boardRow
	for _, r := range rows {
		if r.Incomplete || (stale && r.State != "" && r.State != coherence.StateCurrent) {
			out = append(out, r)
		}
	}
	return out
}

// printBoard renders rows as one table.
func printBoard(ios *iostreams.IOStreams, rows []boardRow) error {
	tp := iostreams.NewTablePrinter(ios)
	tp.AddHeader("City", "Layer", "Rows", "Last Success", "Failing", "State", "Retry")
	for _, r := range rows {
		failing := "-"
		switch {
		case len(r.Failing) > 0:
			failing = strings.Join(r.Failing, ",")
			if r.Error != "" {
				failing += " (" + truncateCell(r.Error, 60) + ")"
			}
		case r.EmptyUpstream:
			failing = "none upstream"
		case r.Incomplete:
			failing = "no rows"
		}
		state := r.stateText
		if state == "" {
			state = "-"
		}
		tp.AddRow(r.City, r.Layer, strconv.Itoa(r.FeatureCount),
			iostreams.FormatTimestamp(r.LastSuccessAt, ios.IsTTY()), failing, state, r.Retry)
	}
	return tp.Render()
}

func truncateCell(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// boardHelp is the --all part of `pvmt status --help`.
const boardHelp = `

With --all, every configured city (or the one --city names) is reported as
one board instead: a row per city and layer (its boundary, roads, parking,
sidewalks, each service's stations and routing roads, and census blocks),
with the rows stored, the last successful fetch, the configured sources whose
last ingest failed and hold none of its rows, the state of the part computed
from it, and the command to run next. A layer is incomplete when a source is
failing, or when it has no rows at all, unless every source answered with
none ("none upstream": a small town with no sidewalks mapped; stations and
routing roads may also legitimately have none). --incomplete keeps only the incomplete rows, --stale
adds those whose part is stale or not computed, and --exit-status exits 1 when
any such row exists. --json takes the board's fields (city, layer,
feature_count, last_success_at, failing_sources, error, state, incomplete,
retry).`

const boardExample = `

  # List what still needs an ingest after a full 'pvmt all ingest'
  pvmt status --all --incomplete

  # Gate a script on every city being ingested and computed
  pvmt status --all --incomplete --stale --exit-status`

// boardCities resolves the cities --all reports on (every configured city, or
// the one --city names) with their stores.
func boardCities(ctx context.Context, f *cmdutil.Factory) ([]boardCity, error) {
	cities, err := cmdutil.ResolveCities(f)
	if err != nil {
		return nil, err
	}
	root, err := f.RootDB()
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	out := make([]boardCity, 0, len(cities))
	for _, city := range cities {
		store, err := cmdutil.EnsureCityStore(ctx, root, city, cmdutil.ResolveConfigID(f.Config, &city))
		if err != nil {
			return nil, err
		}
		out = append(out, boardCity{City: city, Store: store})
	}
	return out, nil
}

// runBoard is `pvmt status --all`.
func runBoard(ctx context.Context, opts *Options) error {
	var cfg *config.Config
	if opts.Config != nil {
		c, err := opts.Config()
		if err != nil {
			return err
		}
		cfg = c
	}
	cities, err := opts.BoardCities(ctx)
	if err != nil {
		return err
	}
	rows, err := boardRows(ctx, cities, cfg)
	if err != nil {
		return err
	}
	flagged := filterBoard(rows, opts.Stale)
	shown := rows
	if opts.Incomplete || opts.Stale {
		shown = flagged
	}
	if opts.Exporter != nil {
		err = cmdutil.WriteRows(opts.IO, opts.Exporter, shown)
	} else {
		err = printBoard(opts.IO, shown)
		fmt.Fprintf(opts.IO.ErrOut, "\n%d of %d cities incomplete\n", incompleteCities(rows), len(cities))
	}
	if err != nil {
		return err
	}
	if opts.ExitStatus && len(flagged) > 0 {
		return cmdutil.ErrSilent
	}
	return nil
}

// boardRows builds every city's rows, with part states under cfg's hashes
// (none without a config).
func boardRows(ctx context.Context, cities []boardCity, cfg *config.Config) ([]boardRow, error) {
	var rows []boardRow
	for _, c := range cities {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hash := ""
		if cfg != nil {
			hash = cfg.CityHash(&c.City)
		}
		cityRows, err := cityBoard(ctx, c, hash)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.City.Slug(), err)
		}
		rows = append(rows, cityRows...)
	}
	return rows, nil
}

// incompleteCities counts the cities with an incomplete layer.
func incompleteCities(rows []boardRow) int {
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Incomplete {
			seen[r.City] = true
		}
	}
	return len(seen)
}
