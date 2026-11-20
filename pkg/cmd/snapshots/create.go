package snapshots

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jcrussell/solvent-streets/internal/coherence"
	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/hint"
	"github.com/jcrussell/solvent-streets/internal/textutil"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

type CreateOptions struct {
	IO          *iostreams.IOStreams
	RootDB      func() (db.RootStorer, error)
	Config      func() (*config.Config, error)
	CityFlagSet func() bool
	Exporter    cmdutil.Exporter
	Name        string
}

// Create statuses, as --json reports them.
const (
	statusCreated = "created"
	statusRefused = "refused"
)

// createRow is one city's outcome.
type createRow struct {
	City     string   `json:"city"`
	Status   string   `json:"status"`
	Snapshot string   `json:"snapshot,omitempty"`
	HexEdgeM float64  `json:"hex_edge_m,omitempty"`
	Reasons  []string `json:"reasons"`
	Warnings []string `json:"warnings"`
	Hint     string   `json:"hint,omitempty"`
}

var _ cmdutil.RowExporter = createRow{}

var createFields = []string{fieldCity, "status", "snapshot", "hex_edge_m", "reasons", "warnings", "hint"}

func (r createRow) ExportData(fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		switch f {
		case fieldCity:
			out[f] = r.City
		case "status":
			out[f] = r.Status
		case "snapshot":
			if r.Snapshot != "" {
				out[f] = r.Snapshot
			}
		case "hex_edge_m":
			if r.HexEdgeM != 0 {
				out[f] = r.HexEdgeM
			}
		case "reasons":
			out[f] = nonNil(r.Reasons)
		case "warnings":
			out[f] = nonNil(r.Warnings)
		case "hint":
			if r.Hint != "" {
				out[f] = r.Hint
			}
		}
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func NewCmdCreate(f *cmdutil.Factory, runF func(context.Context, *CreateOptions) error) *cobra.Command {
	opts := &CreateOptions{
		IO:          f.IOStreams,
		RootDB:      func() (db.RootStorer, error) { return f.RootDB() },
		Config:      func() (*config.Config, error) { return f.Config() },
		CityFlagSet: cityFlagSet(f),
	}

	cmd := &cobra.Command{
		Use:   "create <name>",
		Args:  snapshotNameArgs(1),
		Short: "Freeze every city's latest compute as a named snapshot",
		Long: `Freeze every configured city's latest compute (the newest run of every
part under the current settings) as one immutable snapshot named <name>,
with each city's hex edge and boundary. Export and serve publish the newest
snapshot; computes only update the latest compute.

A name is ` + db.SnapshotNameRule + `,
like a git tag; "latest" is reserved. By convention a snapshot is named
for the month its data was cut ("2026-11"), which keeps it apart from
pvmt's own version and sorts in date order. A snapshot never changes:
creating a name that exists is refused. To replace one, delete it and
create it again; cities that were not recomputed keep their runs, so that
is cheap.

Every city must be coherent, and create is all or nothing: if any city is
refused, no snapshot is created and every refusal is listed. A city is
refused when any part was computed from features or a boundary that have
been ingested again since, when it has no pavement compute, when pavement
has no combined pass, or when a pavement resource was ingested but never
computed. The refusal names what changed and the command that fixes it. A
missing optional part (service coverage, census population) whose inputs
are stored is a warning, not a refusal. --city is not supported: a snapshot
covers every configured city.`,
		Example: `  # Snapshot every configured city
  pvmt snapshots create 2026-11

  # Machine-readable
  pvmt snapshots create 2026-11 --json city,status,reasons,hint`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := rejectCity(opts.CityFlagSet, "create"); err != nil {
				return err
			}
			opts.Name = args[0]
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runCreate(cmd.Context(), opts)
		},
	}

	cmdutil.AddJSONFlags(cmd, &opts.Exporter, createFields)
	return cmd
}

// createCity is one configured city of a create: its row and the store and
// coherence report behind it.
type createCity struct {
	city   *config.CityConfig
	id     int64
	store  db.Store
	report coherence.Report
}

func runCreate(ctx context.Context, opts *CreateOptions) error {
	cfg, err := opts.Config()
	if err != nil {
		return err
	}
	if len(cfg.Cities) == 0 {
		return errors.New("no cities configured")
	}
	root, err := opts.RootDB()
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}

	cities := make([]*createCity, len(cfg.Cities))
	reqs := make([]db.SnapshotRequest, len(cfg.Cities))
	for i := range cfg.Cities {
		city := &cfg.Cities[i]
		id, store, err := db.CityStore(ctx, root, city.Slug(), city.Name, cfg.CityConfigID(city))
		if err != nil {
			return err
		}
		c := &createCity{city: city, id: id, store: store}
		cities[i] = c
		reqs[i] = db.SnapshotRequest{
			CityID:     id,
			ConfigHash: cfg.CityHash(city),
			HexEdgeM:   cfg.ResolvedHexEdge(city),
			Check: func(parts []db.LatestPart, inputs []db.InputState) error {
				c.report = coherence.Check(city, parts, inputs)
				return c.report.Err()
			},
		}
	}

	snap, err := root.CreateSnapshot(ctx, opts.Name, config.ComputeEpoch, reqs)
	var refusedErr *db.SnapshotRefusedError
	switch {
	case errors.Is(err, db.ErrSnapshotExists):
		return hint.Withf(fmt.Errorf("snapshot %q already exists", opts.Name),
			"snapshots never change: replace it with 'pvmt snapshots delete %s', then create it again, or choose another name",
			opts.Name)
	case errors.As(err, &refusedErr):
		return reportRefused(ctx, opts, cfg, cities, refusedErr)
	case err != nil:
		return fmt.Errorf("create snapshot %q: %w", opts.Name, err)
	}

	rows := make([]createRow, len(cities))
	for i, c := range cities {
		rows[i] = createRow{City: c.city.Slug(), Status: statusCreated, Snapshot: snap.Name,
			HexEdgeM: reqs[i].HexEdgeM, Warnings: c.report.Warnings}
	}
	if opts.Exporter != nil {
		return cmdutil.WriteRows(opts.IO, opts.Exporter, rows)
	}
	for _, r := range rows {
		printCreateRow(opts.IO, r)
	}
	fmt.Fprintf(opts.IO.Out, "created %s: %d %s\n", snap.Name, snap.CityCount, textutil.Plural(snap.CityCount, "city", "cities"))
	return nil
}

// reportRefused writes one refused row per refused city (in config order)
// and exits non-zero: nothing was created. When every refused city has
// nothing computed, the error is a missing prerequisite (exit 3) whose hint
// names the compute to run first; any other refusal exits 1.
func reportRefused(ctx context.Context, opts *CreateOptions, cfg *config.Config, cities []*createCity, refused *db.SnapshotRefusedError) error {
	byID := make(map[int64]error, len(refused.Refusals))
	for _, r := range refused.Refusals {
		byID[r.CityID] = r.Err
	}
	var rows []createRow
	onlyUncomputed := true
	for _, c := range cities {
		err, ok := byID[c.id]
		if !ok {
			continue
		}
		row := createRow{City: c.city.Slug(), Status: statusRefused, Warnings: c.report.Warnings}
		var coh *coherence.RefusedError
		switch {
		case errors.As(err, &coh):
			row.Reasons, row.Hint = coh.Report.Refusals, coh.Report.Hint
		case errors.Is(err, db.ErrNothingComputed):
			row.Reasons = []string{err.Error()}
			row.Hint = "pvmt --city " + c.city.Slug() + " all compute"
			// A city with snapshots was computed, under other settings: say
			// which change makes its old results not apply.
			if snaps, lerr := c.store.ListSnapshots(ctx); lerr == nil && len(snaps) > 0 {
				row.Hint = cfg.StaleHint(c.city)
			}
		default:
			row.Reasons = []string{err.Error()}
		}
		onlyUncomputed = onlyUncomputed && errors.Is(err, db.ErrNothingComputed)
		rows = append(rows, row)
	}
	first, result := refusalError(refused.Name, rows, len(cities), onlyUncomputed)
	if opts.Exporter != nil {
		if err := cmdutil.WriteRows(opts.IO, opts.Exporter, rows); err != nil {
			return err
		}
		return result
	}
	for _, r := range rows {
		if r.Hint == first {
			// The returned error's hint names this command already.
			r.Hint = ""
		}
		printCreateRow(opts.IO, r)
	}
	fmt.Fprintf(opts.IO.Out, "refused %q: %d of %d %s refused; nothing was created\n",
		refused.Name, len(rows), len(cities), textutil.Plural(len(cities), "city", "cities"))
	return result
}

// refusalError is a refused create's error: ErrSilent (exit 1), or when
// every refused row has nothing computed, a missing prerequisite (exit 3)
// whose hint names first, the compute to run.
func refusalError(name string, rows []createRow, cityCount int, onlyUncomputed bool) (first string, err error) {
	if !onlyUncomputed || len(rows) == 0 {
		return "", cmdutil.ErrSilent
	}
	first = "pvmt all compute"
	if len(rows) == 1 {
		first = "pvmt --city " + rows[0].City + " all compute"
	}
	return first, cmdutil.NeedsFirst(fmt.Errorf("snapshot %q refused: nothing computed under the current settings in %d of %d %s",
		name, len(rows), cityCount, textutil.Plural(cityCount, "city", "cities")), first)
}

// hintSentence is a row's hint as printed: a bare command (the --json hint
// value) becomes "run '<command>'"; prose is printed as it is.
func hintSentence(h string) string {
	if strings.HasPrefix(h, "pvmt ") {
		return "run '" + h + "'"
	}
	return h
}

// printCreateRow writes a city's refusal ("oakland-ca: refused: <reasons>")
// to out, and its hint and warnings to errOut. A created city with no
// warnings prints nothing: the summary line names the snapshot.
func printCreateRow(ios *iostreams.IOStreams, r createRow) {
	if r.Status == statusRefused {
		fmt.Fprintf(ios.Out, "%s: refused: %s\n", r.City, strings.Join(r.Reasons, "; "))
		if r.Hint != "" {
			fmt.Fprintf(ios.ErrOut, "hint: %s\n", hintSentence(r.Hint))
		}
	}
	for _, w := range r.Warnings {
		cmdutil.Warnf(ios, "%s: %s", r.City, w)
	}
}
