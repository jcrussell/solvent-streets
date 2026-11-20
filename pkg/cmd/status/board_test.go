package status

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// albanyBoard builds the 2026-11-09 Albany case in a real store: OSM roads
// timed out while ArcGIS answered, OSM sidewalks got a 502 and nothing was
// stored, parking and the fire stations came in, and census was never
// ingested. Lakeside is a second, complete city.
func albanyBoard(t *testing.T) []boardCity {
	t.Helper()
	ctx := context.Background()
	root, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	city := func(name string) (config.CityConfig, db.Store) {
		c := config.CityConfig{Name: name, Overpass: true, ArcGISURL: "https://example.com/query"}
		id, err := root.EnsureCity(ctx, c.Slug(), c.Name, "test")
		if err != nil {
			t.Fatal(err)
		}
		store := root.ForCity(id)
		if err := store.SaveBoundary(ctx, `{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]}`, "nominatim"); err != nil {
			t.Fatal(err)
		}
		return c, store
	}
	feature := func(id string, rt resource.Type, src string) db.Feature {
		return db.Feature{ID: id, ResourceType: rt, Tags: map[string]string{"highway": "residential"},
			GeometryJSON: `{"type":"LineString","coordinates":[[0,0],[1,1]]}`, SourceAPI: src, FetchedAt: time.Now()}
	}
	now := time.Now()
	attempt := func(input, src string, outcome db.IngestOutcome, n int, msg string) db.IngestAttempt {
		return db.IngestAttempt{Input: input, Source: src, StartedAt: now, FinishedAt: now, Outcome: outcome, FeatureCount: n, Error: msg}
	}

	albany, as := city("Albany, CA")
	for _, f := range []struct {
		rt  resource.Type
		src string
	}{{resource.TypeRoads, "arcgis"}, {resource.TypeParking, "overpass"}, {resource.FacilityType("fire"), "overpass"}} {
		if err := as.UpsertFeatures(ctx, f.rt, []db.Feature{feature("a", f.rt, f.src)}, []string{f.src}); err != nil {
			t.Fatal(err)
		}
	}
	if err := as.RecordIngestAttempts(ctx, []db.IngestAttempt{
		attempt("boundary", "nominatim", db.IngestOK, 1, ""),
		attempt("roads", "overpass", db.IngestFailed, 0, "http2: timeout awaiting response headers"),
		attempt("roads", "arcgis", db.IngestOK, 1, ""),
		attempt("parking", "overpass", db.IngestOK, 1, ""),
		attempt("parking", "arcgis", db.IngestEmpty, 0, ""),
		attempt("sidewalks", "overpass", db.IngestFailed, 0, "502 Gateway Timeout"),
		attempt("sidewalks", "arcgis", db.IngestEmpty, 0, ""),
	}); err != nil {
		t.Fatal(err)
	}

	lakeside, ls := city("Lakeside, CA")
	lakeside.Census = new(bool) // opted out
	for _, rt := range []resource.Type{resource.TypeRoads, resource.TypeParking, resource.TypeSidewalks} {
		if err := ls.UpsertFeatures(ctx, rt, []db.Feature{feature("b", rt, "overpass")}, []string{"overpass"}); err != nil {
			t.Fatal(err)
		}
	}
	return []boardCity{{City: albany, Store: as}, {City: lakeside, Store: ls}}
}

func boardOpts(t *testing.T, cities []boardCity) (*Options, func() (string, string)) {
	t.Helper()
	ios, _, out, errOut := iostreams.Test()
	return &Options{
		IO:          ios,
		All:         true,
		BoardCities: func(context.Context) ([]boardCity, error) { return cities, nil },
	}, func() (string, string) { return out.String(), errOut.String() }
}

// TestBoard_IncompleteLayers: --incomplete lists
// exactly the layers needing an ingest, each with its failing source, the
// error and the command that retries only that source.
func TestBoard_IncompleteLayers(t *testing.T) {
	opts, _ := boardOpts(t, albanyBoard(t))
	opts.Incomplete = true
	exporter := &recordingExporter{fields: []string{"city", "layer", "failing_sources", "error", "incomplete", "hint"}}
	opts.Exporter = exporter
	if err := runBoard(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	rows := exporter.rows
	var got []string
	for _, r := range rows {
		got = append(got, r["city"].(string)+"/"+r["layer"].(string)+" -> "+r["hint"].(string))
	}
	want := []string{
		"albany-ca/roads -> pvmt --city albany-ca roads ingest --source overpass",
		"albany-ca/sidewalks -> pvmt --city albany-ca sidewalks ingest --source overpass",
		"albany-ca/layer-census-blocks -> pvmt --city albany-ca census ingest",
	}
	if !slices.Equal(got, want) {
		t.Errorf("incomplete rows =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if len(rows) > 0 && !strings.Contains(rows[0]["error"].(string), "overpass: http2: timeout") {
		t.Errorf("roads error = %v, want the overpass timeout", rows[0]["error"])
	}
}

// TestBoard_TableAndExitStatus: the table covers every city and layer and
// counts the incomplete cities; --exit-status fails the run only while a
// layer is incomplete.
func TestBoard_TableAndExitStatus(t *testing.T) {
	cities := albanyBoard(t)
	opts, output := boardOpts(t, cities)
	opts.ExitStatus = true
	err := runBoard(context.Background(), opts)
	if !errors.Is(err, cmdutil.ErrSilent) {
		t.Errorf("runBoard = %v, want ErrSilent (exit 1) while Albany is incomplete", err)
	}
	out, errOut := output()
	for _, want := range []string{"albany-ca", "lakeside-ca", "facility-fire", "routing-roads", "502 Gateway Timeout"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "lakeside-ca  layer-census-blocks") {
		t.Errorf("census listed for a city that opted out:\n%s", out)
	}
	if !strings.Contains(errOut, "1 of 2 cities incomplete") {
		t.Errorf("summary missing: %q", errOut)
	}

	opts, _ = boardOpts(t, cities[1:])
	opts.ExitStatus = true
	if err := runBoard(context.Background(), opts); err != nil {
		t.Errorf("runBoard on a complete city = %v, want success", err)
	}
}

// TestNewCmdStatus_BoardFlagsNeedAll: the board filters are usage errors
// without --all, and a resource's status has no --all.
func TestNewCmdStatus_BoardFlagsNeedAll(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}
	cmd := NewCmdStatus(f, nil, func(context.Context, *Options) error { return nil })
	cmd.SetArgs([]string{"--incomplete"})
	cmd.SetOut(ios.Out)
	cmd.SetErr(ios.ErrOut)
	var flagErr *cmdutil.FlagError
	if err := cmd.Execute(); !errors.As(err, &flagErr) {
		t.Errorf("--incomplete without --all = %v, want a flag error", err)
	}
	if NewCmdStatus(f, &resource.Pavement{}, nil).Flags().Lookup("all") != nil {
		t.Error("pvmt roads status has --all")
	}
}

// recordingExporter keeps the rows a command writes.
type recordingExporter struct {
	fields []string
	rows   []map[string]any
}

func (e *recordingExporter) Fields() []string { return e.fields }
func (e *recordingExporter) Write(_ *iostreams.IOStreams, rows []map[string]any) error {
	e.rows = rows
	return nil
}

// TestBoard_EmptyUpstreamIsNotIncomplete: a layer with no rows because every
// configured source answered with none is marked, not listed for a retry
// (the 2026-11-15 Springfields pre-flight: small towns with no sidewalks
// mapped).
func TestBoard_EmptyUpstreamIsNotIncomplete(t *testing.T) {
	ctx := context.Background()
	root, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	c := config.CityConfig{Name: "Springfield, GA", Overpass: true, Census: new(bool)}
	id, err := root.EnsureCity(ctx, c.Slug(), c.Name, "test")
	if err != nil {
		t.Fatal(err)
	}
	store := root.ForCity(id)
	now := time.Now()
	if err := store.RecordIngestAttempts(ctx, []db.IngestAttempt{
		{Input: "sidewalks", Source: "overpass", StartedAt: now, FinishedAt: now, Outcome: db.IngestEmpty},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := cityBoard(ctx, boardCity{City: c, Store: store}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		switch r.Layer {
		case "sidewalks":
			if r.Incomplete || !r.EmptyUpstream || r.Hint != "" {
				t.Errorf("sidewalks = %+v, want empty upstream, not incomplete", r)
			}
		case "parking": // never ingested: still a gap
			if !r.Incomplete || r.EmptyUpstream {
				t.Errorf("parking = %+v, want incomplete", r)
			}
		}
	}
}
