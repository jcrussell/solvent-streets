package db

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/resource"
)

// timestampColumns is every timestamp column of the schema, as table.column.
var timestampColumns = []string{
	"city_boundaries.fetched_at",
	"features.fetched_at",
	"city_inputs.ingested_at",
	"city_inputs.fetched_at",
	"ingest_attempts.started_at",
	"ingest_attempts.finished_at",
	"runs.computed_at",
	"snapshots.created_at",
}

// TestTimestamps_SchemaIsInteger: every *_at column is declared INTEGER, and
// the list above names all of them.
func TestTimestamps_SchemaIsInteger(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	var found []string
	for _, table := range listTables(t, ctx, root.db) {
		for _, c := range listColumns(t, ctx, root.db, table) {
			if !strings.HasSuffix(c.name, "_at") {
				continue
			}
			found = append(found, table+"."+c.name)
			if c.typ != "INTEGER" {
				t.Errorf("%s.%s is %s, want INTEGER unix seconds", table, c.name, c.typ)
			}
		}
	}
	for _, want := range timestampColumns {
		if !strings.Contains(strings.Join(found, " "), want) {
			t.Errorf("timestamp column %s missing; found %v", want, found)
		}
	}
	if len(found) != len(timestampColumns) {
		t.Errorf("timestamp columns = %v, want exactly %v", found, timestampColumns)
	}
}

// TestTimestamps_EveryWriterStoresInteger exercises every statement that
// writes a timestamp and checks each stored value is an integer, so no two
// rows can hold the same instant in different text forms (solvent-streets-9uu5:
// gc compared Go's time.String() against CURRENT_TIMESTAMP text).
func TestTimestamps_EveryWriterStoresInteger(t *testing.T) {
	ctx := context.Background()
	root, store, _ := openInputsStore(t)
	if err := store.SaveBoundary(ctx, `{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]}`, "nominatim"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertFeatures(ctx, resource.TypeRoads, []Feature{feature("w1", "overpass", geomA)}, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := store.RecordIngestAttempts(ctx, []IngestAttempt{{Input: "roads", Source: "overpass", StartedAt: now, FinishedAt: now, Outcome: IngestOK}}); err != nil {
		t.Fatal(err)
	}
	commitRun(t, store, "h", "roads", nil) // a run and a snapshot

	for _, col := range timestampColumns {
		table, column, _ := strings.Cut(col, ".")
		var rows, ints int
		q := `SELECT COUNT(*), COUNT(CASE WHEN typeof(` + column + `) = 'integer' THEN 1 END) FROM ` + table
		if err := root.db.QueryRowContext(ctx, q).Scan(&rows, &ints); err != nil {
			t.Fatalf("%s: %v", col, err)
		}
		if rows == 0 {
			t.Errorf("%s: no rows written; the test no longer exercises its writer", col)
		}
		if ints != rows {
			t.Errorf("%s: %d of %d values are integers", col, ints, rows)
		}
		var minAt, maxAt int64
		if err := root.db.QueryRowContext(ctx, `SELECT MIN(`+column+`), MAX(`+column+`) FROM `+table).Scan(&minAt, &maxAt); err != nil {
			t.Fatalf("%s: %v", col, err)
		}
		if lo, hi := now.Add(-time.Minute).Unix(), now.Add(time.Minute).Unix(); minAt < lo || maxAt > hi {
			t.Errorf("%s spans [%d, %d], want unix seconds near %d", col, minAt, maxAt, now.Unix())
		}
	}
}

// TestStats_LastIngestAt: FeatureCount counts the stored rows, and
// LastIngestAt is the newest fetched_at of the stored
// rows, as before city_inputs kept it. A partial replace keeps the other
// source's rows (and so its time when that is newer), an identical re-ingest
// moves it, and a type whose rows are all gone has none.
func TestStats_LastIngestAt(t *testing.T) {
	ctx := context.Background()
	_, store, _ := openInputsStore(t)
	rt := resource.TypeRoads
	at := func(s int64) time.Time { return time.Unix(s, 0) }
	stored := 0 // rows the store should hold; Stats' count must match
	last := func() *time.Time {
		t.Helper()
		info, err := store.Stats(ctx, rt)
		if err != nil {
			t.Fatal(err)
		}
		if info.FeatureCount != stored {
			t.Errorf("FeatureCount = %d, want %d", info.FeatureCount, stored)
		}
		return info.LastIngestAt
	}
	if got := last(); got != nil {
		t.Fatalf("never ingested: LastIngestAt = %v, want nil", got)
	}

	osm, arc := feature("w1", "overpass", geomA), feature("a1", "arcgis", geomB)
	osm.FetchedAt, arc.FetchedAt = at(1000), at(2000)
	if err := store.UpsertFeatures(ctx, rt, []Feature{osm, arc}, nil); err != nil {
		t.Fatal(err)
	}
	stored = 2
	if got := last(); got == nil || !got.Equal(at(2000)) || got.Location() != time.UTC {
		t.Fatalf("after both sources: LastIngestAt = %v, want %v UTC", got, at(2000))
	}

	// Overpass alone re-ingested, earlier than arcgis's rows: arcgis's rows
	// survive, so the newest stored row is still arcgis's.
	osm.FetchedAt = at(1500)
	if err := store.UpsertFeatures(ctx, rt, []Feature{osm}, []string{"overpass"}); err != nil {
		t.Fatal(err)
	}
	if got := last(); got == nil || !got.Equal(at(2000)) {
		t.Errorf("after a partial replace: LastIngestAt = %v, want %v", got, at(2000))
	}

	// An identical re-ingest keeps the digest but rewrites the rows.
	osm.FetchedAt, arc.FetchedAt = at(3000), at(3000)
	if err := store.UpsertFeatures(ctx, rt, []Feature{osm, arc}, nil); err != nil {
		t.Fatal(err)
	}
	if got := last(); got == nil || !got.Equal(at(3000)) {
		t.Errorf("after an identical re-ingest: LastIngestAt = %v, want %v", got, at(3000))
	}

	// A replace that saves nothing leaves no rows, and so no ingest time.
	if err := store.UpsertFeatures(ctx, rt, nil, nil); err != nil {
		t.Fatal(err)
	}
	stored = 0
	if got := last(); got != nil {
		t.Errorf("with no rows: LastIngestAt = %v, want nil", got)
	}
}

// TestStats_LastIngestAtFollowsGC: a gc sweep that deletes the newest rows
// moves LastIngestAt back to the newest surviving row.
func TestStats_LastIngestAtFollowsGC(t *testing.T) {
	ctx := context.Background()
	_, store, _ := openInputsStore(t)
	rt := resource.TypeRoads
	osm, arc := feature("w1", "overpass", geomA), feature("a1", "arcgis", geomB)
	osm.FetchedAt, arc.FetchedAt = time.Unix(1000, 0), time.Unix(2000, 0)
	if err := store.UpsertFeatures(ctx, rt, []Feature{osm, arc}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GCSweep(ctx, []string{"overpass"}, testEpoch); err != nil {
		t.Fatal(err)
	}
	info, err := store.Stats(ctx, rt)
	if err != nil {
		t.Fatal(err)
	}
	if info.LastIngestAt == nil || !info.LastIngestAt.Equal(time.Unix(1000, 0)) || info.FeatureCount != 1 {
		t.Errorf("after sweeping arcgis: %+v, want 1 feature last ingested at %v", info, time.Unix(1000, 0))
	}
}
