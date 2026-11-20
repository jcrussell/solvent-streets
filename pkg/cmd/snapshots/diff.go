package snapshots

import (
	"context"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/export"
	"github.com/jcrussell/solvent-streets/internal/population"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/results"
	"github.com/jcrussell/solvent-streets/internal/services"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

type DiffOptions struct {
	IO            *iostreams.IOStreams
	RootDB        func() (db.RootStorer, error)
	Config        func() (*config.Config, error)
	ResolveCities func() ([]config.CityConfig, error)
	CityFlagSet   func() bool
	UnitSystem    func() units.System
	Exporter      cmdutil.Exporter
	A, B          string
}

// Diff statuses: a city in both snapshots is changed or unchanged; one in
// only one of them is added (only in B) or removed (only in A).
const (
	statusChanged   = "changed"
	statusUnchanged = "unchanged"
	statusAdded     = "added"
	statusRemoved   = "removed"
)

// measureRow is one computed result of a city in both snapshots, keyed like
// the stored results: metric over subject (city_metrics' keys for pavement,
// the per-hex metric key for census and services). A and B are omitted where
// the snapshot holds no such result; Delta and DeltaShare (Delta as a
// fraction of A) where either is missing or A is 0.
type measureRow struct {
	Metric     string   `json:"metric"`
	Subject    string   `json:"subject"`
	A          *float64 `json:"a,omitempty"`
	B          *float64 `json:"b,omitempty"`
	Delta      *float64 `json:"delta,omitempty"`
	DeltaShare *float64 `json:"delta_share,omitempty"`
}

// diffRow is one city's comparison.
type diffRow struct {
	City   string
	Status string
	// Paved is the combined paved area, every jurisdiction (results.PavedArea),
	// the row's headline and sort key.
	Paved             measureRow
	HexEdgeA          float64
	HexEdgeB          float64
	HexEdgeChanged    bool
	BoundaryChanged   bool
	ConfigHashChanged bool
	ComputeEpochA     int
	ComputeEpochB     int
	Measures          []measureRow
	// fire is the fire coverage summary of each side, for the table.
	fireA, fireB *export.MetricSummaryJSON
	popDelta     *float64
}

var _ cmdutil.RowExporter = diffRow{}

var diffFields = []string{
	fieldCity, "status", "total_paved_a", "total_paved_b", "total_paved_delta", "total_paved_delta_share",
	"hex_edge_m_a", "hex_edge_m_b", "hex_edge_changed", "boundary_changed", "config_hash_changed",
	"compute_epoch_a", "compute_epoch_b", "measures",
}

func (r diffRow) ExportData(fields []string) map[string]any {
	all := r.values()
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		if v, ok := all[f]; ok {
			out[f] = v
		}
	}
	return out
}

// values is every --json field of the row that applies to it: a city in
// only one snapshot has no comparison, and a side without a value omits it.
func (r diffRow) values() map[string]any {
	out := map[string]any{fieldCity: r.City, "status": r.Status}
	put := func(f string, v *float64) {
		if v != nil {
			out[f] = *v
		}
	}
	put("total_paved_a", r.Paved.A)
	put("total_paved_b", r.Paved.B)
	put("total_paved_delta", r.Paved.Delta)
	put("total_paved_delta_share", r.Paved.DeltaShare)
	if r.Status != statusAdded {
		out["hex_edge_m_a"], out["compute_epoch_a"] = r.HexEdgeA, r.ComputeEpochA
	}
	if r.Status != statusRemoved {
		out["hex_edge_m_b"], out["compute_epoch_b"] = r.HexEdgeB, r.ComputeEpochB
	}
	if r.Status == statusChanged || r.Status == statusUnchanged {
		out["hex_edge_changed"] = r.HexEdgeChanged
		out["boundary_changed"] = r.BoundaryChanged
		out["config_hash_changed"] = r.ConfigHashChanged
		out["measures"] = r.Measures
	}
	return out
}

func NewCmdDiff(f *cmdutil.Factory, runF func(context.Context, *DiffOptions) error) *cobra.Command {
	opts := &DiffOptions{
		IO:            f.IOStreams,
		RootDB:        func() (db.RootStorer, error) { return f.RootDB() },
		Config:        func() (*config.Config, error) { return f.Config() },
		ResolveCities: func() ([]config.CityConfig, error) { return cmdutil.ResolveCities(f) },
		CityFlagSet:   cityFlagSet(f),
		UnitSystem:    f.UnitSystem,
	}

	cmd := &cobra.Command{
		Use:               "diff <a> <b>",
		Args:              snapshotNameArgs(2),
		ValidArgsFunction: cmdutil.SnapshotNameCompletion(f, 2),
		Short:             "Compare two snapshots city by city",
		Long: `Compare the computed results of two snapshots, one row per city: the paved
area, feature count and parking spaces of each resource and the combined
pass, every jurisdiction and city-maintained; census population and housing
units, summed over the hexes; and fire coverage, the share of the network
within the threshold and the median response, as the site's Map tab shows
them. Each row flags a changed hex edge, boundary or config hash, and gives
both snapshots' compute epochs (a pvmt release that computes differently).
A city in only one snapshot is listed as added or removed.

Rows are sorted by the largest relative change in combined paved area. Use
--city to compare one city, and --json for every measure. Exits 3 when no
city is in both snapshots. A hex-by-hex comparison is not offered: two
snapshots at different hex edges have no hexes in common.`,
		Example: `  # Compare a release with the one before it
  pvmt snapshots diff 2026-11 2027-02

  # One city
  pvmt --city oakland-ca snapshots diff 2026-11 2027-02

  # Every measure, machine-readable
  pvmt snapshots diff 2026-11 2027-02 --json city,status,measures`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.A, opts.B = args[0], args[1]
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runDiff(cmd.Context(), opts)
		},
	}

	cmdutil.AddJSONFlags(cmd, &opts.Exporter, diffFields)
	return cmd
}

func runDiff(ctx context.Context, opts *DiffOptions) error {
	cfg, err := opts.Config()
	if err != nil {
		return err
	}
	root, err := opts.RootDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	a, err := lookupSnapshot(ctx, root, opts.A)
	if err != nil {
		return err
	}
	b, err := lookupSnapshot(ctx, root, opts.B)
	if err != nil {
		return err
	}
	citiesA, citiesB := a.Cities, b.Cities
	if opts.CityFlagSet != nil && opts.CityFlagSet() {
		selected, err := opts.ResolveCities()
		if err != nil {
			return err
		}
		citiesA, citiesB = filterCities(citiesA, selected), filterCities(citiesB, selected)
	}

	rows, compared, err := diffCities(ctx, root, cfg, a, b, citiesA, citiesB)
	if err != nil {
		return err
	}

	if opts.Exporter != nil {
		if err := cmdutil.WriteRows(opts.IO, opts.Exporter, rows); err != nil {
			return err
		}
	} else if err := printDiff(opts, a, b, rows, compared); err != nil {
		return err
	}
	if compared == 0 {
		cmdutil.Warnf(opts.IO, "no city is in both %s and %s", a.Name, b.Name)
		return cmdutil.ErrNoResults
	}
	return nil
}

// diffParallelism bounds how many cities diffCities compares at once. Each
// comparison is mostly one drawn-grid build per side (export.MetricSummaries),
// which already spreads its clip over every core, so a few at a time keep the
// cores busy through the reads and parses between clips without holding
// many cities' grids in memory.
const diffParallelism = 4

// diffCities compares every city of citiesA (of snapshot a) and citiesB (of
// b): the ones in both first, the largest relative change in paved area
// first, then the ones in only one, by slug. compared counts the first.
// Cities are compared concurrently (diffParallelism); the order does not
// depend on which finishes first.
func diffCities(ctx context.Context, root db.RootStorer, cfg *config.Config, a, b *db.Snapshot, citiesA, citiesB []db.SnapshotCity) (rows []diffRow, compared int, err error) {
	inB := make(map[int64]db.SnapshotCity, len(citiesB))
	for _, c := range citiesB {
		inB[c.CityID] = c
	}
	type pair struct{ a, b db.SnapshotCity }
	var both []pair
	var onlyOne []diffRow
	for _, ca := range citiesA {
		cb, ok := inB[ca.CityID]
		if !ok {
			onlyOne = append(onlyOne, diffRow{City: ca.CitySlug, Status: statusRemoved, HexEdgeA: ca.HexEdgeM, ComputeEpochA: a.ComputeEpoch})
			continue
		}
		delete(inB, ca.CityID)
		both = append(both, pair{ca, cb})
	}
	for _, cb := range citiesB {
		if _, ok := inB[cb.CityID]; ok {
			onlyOne = append(onlyOne, diffRow{City: cb.CitySlug, Status: statusAdded, HexEdgeB: cb.HexEdgeM, ComputeEpochB: b.ComputeEpoch})
		}
	}

	rows = make([]diffRow, len(both))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(diffParallelism)
	for i, p := range both {
		g.Go(func() error {
			row, err := diffCity(gctx, root, cfg, p.a, p.b)
			if err != nil {
				return err
			}
			row.ComputeEpochA, row.ComputeEpochB = a.ComputeEpoch, b.ComputeEpoch
			rows[i] = row
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, 0, err
	}
	slices.SortStableFunc(rows, func(x, y diffRow) int {
		if c := -compareChange(x.Paved, y.Paved); c != 0 {
			return c
		}
		return strings.Compare(x.City, y.City)
	})
	slices.SortStableFunc(onlyOne, func(x, y diffRow) int { return strings.Compare(x.City, y.City) })
	return slices.Concat(rows, onlyOne), len(rows), nil
}

// sameResults reports whether two snapshots hold the same results for a
// city: every part read from the same run, drawn on the same boundary at the
// same hex edge. Everything loadMeasures reads is a function of those (and
// of the config, which both sides share), so such a city's measures are read
// once and are equal by construction. Two snapshots of a release share most
// of their runs, so this skips most of a diff's drawn-grid builds.
func sameResults(ca, cb db.SnapshotCity) bool {
	if ca.CityID != cb.CityID || ca.BoundaryDigest != cb.BoundaryDigest || ca.HexEdgeM != cb.HexEdgeM {
		return false
	}
	runs := func(c db.SnapshotCity) map[string]int64 {
		m := make(map[string]int64, len(c.Parts))
		for _, p := range c.Parts {
			m[p.Part] = p.RunID
		}
		return m
	}
	return len(ca.Parts) == len(cb.Parts) && maps.Equal(runs(ca), runs(cb))
}

// diffCity compares one city's results in two snapshots, each read through
// the city's store pinned to that snapshot (once, when both hold the same
// results: sameResults).
func diffCity(ctx context.Context, root db.RootStorer, cfg *config.Config, ca, cb db.SnapshotCity) (diffRow, error) {
	city := config.CityConfig{Name: ca.CitySlug}
	for _, c := range cfg.Cities {
		if c.Slug() == ca.CitySlug {
			city = c
			break
		}
	}
	entry := func(snap db.SnapshotCity) export.CityEntry {
		return export.CityEntry{Config: cfg, City: city, Slug: snap.CitySlug, Store: root.ForCity(snap.CityID).WithSnapshot(snap.ID)}
	}
	ma, err := loadMeasures(ctx, entry(ca))
	if err != nil {
		return diffRow{}, fmt.Errorf("%s in %s: %w", ca.CitySlug, ca.Name, err)
	}
	mb := ma
	if !sameResults(ca, cb) {
		if mb, err = loadMeasures(ctx, entry(cb)); err != nil {
			return diffRow{}, fmt.Errorf("%s in %s: %w", cb.CitySlug, cb.Name, err)
		}
	}
	row := diffRow{
		City:              ca.CitySlug,
		HexEdgeA:          ca.HexEdgeM,
		HexEdgeB:          cb.HexEdgeM,
		HexEdgeChanged:    ca.HexEdgeM != cb.HexEdgeM,
		BoundaryChanged:   ca.BoundaryDigest != cb.BoundaryDigest,
		ConfigHashChanged: ca.ConfigHash != cb.ConfigHash,
		fireA:             ma.fire,
		fireB:             mb.fire,
	}
	changed := row.HexEdgeChanged || row.BoundaryChanged || row.ConfigHashChanged
	for _, k := range measureKeys(ma, mb) {
		m := compare(k, ma, mb)
		if m.Delta == nil || *m.Delta != 0 || (m.A == nil) != (m.B == nil) {
			changed = true
		}
		row.Measures = append(row.Measures, m)
		if k == pavedKey {
			row.Paved = m
		}
		if k.metric == population.KeyPopulation {
			row.popDelta = m.Delta
		}
	}
	row.Status = statusUnchanged
	if changed {
		row.Status = statusChanged
	}
	return row, nil
}

// measureKey names one measure: metric over subject.
type measureKey struct{ metric, subject string }

// pavedKey is the headline: the combined paved area over every jurisdiction.
var pavedKey = measureKey{resource.MetricPavedArea, string(resource.TypeCombined)}

// fire* name the fire coverage measures: the median response and the share
// of the network within the threshold.
var (
	fireMedianKey = measureKey{services.MetricFire, "median"}
	fireWithinKey = measureKey{"within_threshold_share", services.MetricFire}
)

// measures is one side of a city's comparison.
type measures struct {
	values map[measureKey]float64
	fire   *export.MetricSummaryJSON
}

// loadMeasures reads every compared result of entry's snapshot.
func loadMeasures(ctx context.Context, entry export.CityEntry) (measures, error) {
	m := measures{values: map[measureKey]float64{}}
	types := append(resource.AllTypes(resource.ScopeAll, resource.ScopeCity),
		resource.TypeCombined, resource.TypeCombined.With(resource.ScopeCity))
	totals, err := results.LoadTotals(ctx, entry.Store, types)
	if err != nil {
		return m, err
	}
	for t, tot := range totals {
		// The stored rows' own shape: area and feature count, and spaces for
		// parking.
		for _, row := range results.TotalRows(t, tot) {
			m.values[measureKey{row.Metric, row.Subject}] = row.Value
		}
	}
	// The headline falls back to the per-resource sum when the combined pass
	// is missing (results.PavedArea), as the site's totals do.
	if _, ok := m.values[pavedKey]; !ok && len(totals) > 0 {
		area, _, err := results.PavedArea(ctx, entry.Store, resource.ScopeAll)
		if err != nil {
			return m, err
		}
		m.values[pavedKey] = area
	}
	sums, err := export.MetricSummaries(ctx, entry)
	if err != nil {
		return m, err
	}
	for _, key := range []string{population.KeyPopulation, population.KeyHousingUnits} {
		if s, ok := sums[key]; ok && s.Total != nil {
			m.values[measureKey{key, ""}] = *s.Total
		}
	}
	if s, ok := sums[services.MetricFire]; ok {
		m.fire = &s
		m.values[fireMedianKey] = s.Median
		if s.WithinThresholdShare != nil {
			m.values[fireWithinKey] = *s.WithinThresholdShare
		}
	}
	return m, nil
}

// measureKeys is every key either side holds, the headline first, then
// pavement by subject and metric, then census and fire.
func measureKeys(a, b measures) []measureKey {
	seen := map[measureKey]bool{}
	var keys []measureKey
	for _, m := range []measures{a, b} {
		for k := range m.values {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	rank := func(k measureKey) int {
		switch {
		case k == pavedKey:
			return 0
		case k.metric == resource.MetricPavedArea || k.metric == resource.MetricFeatureCount || k.metric == resource.MetricParkingSpaces:
			return 1
		case k.subject == "":
			return 2
		}
		return 3
	}
	slices.SortFunc(keys, func(x, y measureKey) int {
		if c := rank(x) - rank(y); c != 0 {
			return c
		}
		if c := strings.Compare(x.subject, y.subject); c != 0 {
			return c
		}
		return strings.Compare(x.metric, y.metric)
	})
	return keys
}

// compare builds a measure's row from each side's value; a side that holds
// none leaves its value, and the delta, out.
func compare(k measureKey, a, b measures) measureRow {
	m := measureRow{Metric: k.metric, Subject: k.subject}
	va, okA := a.values[k]
	vb, okB := b.values[k]
	if okA {
		m.A = &va
	}
	if okB {
		m.B = &vb
	}
	if okA && okB {
		d := vb - va
		m.Delta = &d
		if va != 0 {
			share := d / va
			m.DeltaShare = &share
		}
	}
	return m
}

// change is how far a measure moved, for sorting: |delta_share|, +Inf for a
// value that appeared from 0 or from nothing, -1 when it cannot be compared.
func change(m measureRow) float64 {
	switch {
	case m.DeltaShare != nil:
		return math.Abs(*m.DeltaShare)
	case m.Delta != nil && *m.Delta != 0, (m.A == nil) != (m.B == nil):
		return math.Inf(1)
	case m.Delta != nil:
		return 0
	}
	return -1
}

func compareChange(x, y measureRow) int {
	cx, cy := change(x), change(y)
	switch {
	case cx < cy:
		return -1
	case cx > cy:
		return 1
	}
	return 0
}

// printDiff writes the comparison as a table: one row per city, the largest
// relative change in paved area first, then the cities in only one snapshot.
func printDiff(opts *DiffOptions, a, b *db.Snapshot, rows []diffRow, compared int) error {
	ios := opts.IO
	sys := units.Metric
	if opts.UnitSystem != nil {
		sys = opts.UnitSystem()
	}
	fmt.Fprintf(ios.ErrOut, "%s (compute epoch %d) -> %s (compute epoch %d): %d %s in both, %d only in one\n",
		a.Name, a.ComputeEpoch, b.Name, b.ComputeEpoch, compared, cityNoun(compared), len(rows)-compared)
	if a.ComputeEpoch != b.ComputeEpoch {
		fmt.Fprintf(ios.ErrOut, "The compute epochs differ: pvmt's compute logic changed between them, so results can move with no data change.\n")
	}
	if len(rows) == 0 {
		return nil
	}
	tp := iostreams.NewTablePrinter(ios)
	tp.AddHeader("City", "Status", "Paved A", "Paved B", "Change", "Population Change", "Fire Within", "Settings")
	for _, r := range rows {
		tp.AddRow(r.City, r.Status, areaCell(r.Paved.A, sys), areaCell(r.Paved.B, sys), changeCell(r.Paved, sys),
			signedCell(r.popDelta), fireCell(r.fireA, r.fireB), settingsCell(r))
	}
	return tp.Render()
}

func areaCell(v *float64, sys units.System) string {
	if v == nil {
		return "-"
	}
	return units.FormatAreaLarge(*v, sys)
}

// changeCell is the paved area's delta and its relative change.
func changeCell(m measureRow, sys units.System) string {
	if m.Delta == nil {
		return "-"
	}
	sign := ""
	if *m.Delta > 0 {
		sign = "+"
	}
	out := sign + units.FormatAreaLarge(*m.Delta, sys)
	if m.DeltaShare != nil {
		out += fmt.Sprintf(" (%+.1f%%)", 100**m.DeltaShare)
	}
	return out
}

func signedCell(v *float64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatFloat(math.Round(*v), 'f', 0, 64)
}

// fireCell is the share of the network within the fire threshold, A -> B,
// as the Map tab's "Within m:ss" row shows it.
func fireCell(a, b *export.MetricSummaryJSON) string {
	share := func(s *export.MetricSummaryJSON) string {
		if s == nil || s.WithinThresholdShare == nil {
			return "-"
		}
		return fmt.Sprintf("%.1f%%", 100**s.WithinThresholdShare)
	}
	if a == nil && b == nil {
		return "-"
	}
	return share(a) + " -> " + share(b)
}

// settingsCell names what the city was computed on that changed.
func settingsCell(r diffRow) string {
	var out []string
	if r.HexEdgeChanged {
		out = append(out, fmt.Sprintf("hex edge %s -> %s m", strconv.FormatFloat(r.HexEdgeA, 'f', -1, 64), strconv.FormatFloat(r.HexEdgeB, 'f', -1, 64)))
	}
	if r.BoundaryChanged {
		out = append(out, "boundary")
	}
	if r.ConfigHashChanged {
		out = append(out, "config hash")
	}
	if len(out) == 0 {
		return "-"
	}
	return strings.Join(out, ", ")
}
