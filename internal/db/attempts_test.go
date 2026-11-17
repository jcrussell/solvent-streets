package db

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// TestIngestHistory_LatestSuccessAndFailure (solvent-streets-o831.1): the
// history of each (input, source) names its newest attempt and its newest
// success and failure, so a source that failed after succeeding reads as
// failing while its last success is still known.
func TestIngestHistory_LatestSuccessAndFailure(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	t0 := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	at := func(input, source string, minute int, outcome IngestOutcome, n int, msg string) IngestAttempt {
		return IngestAttempt{Input: input, Source: source, StartedAt: t0.Add(time.Duration(minute) * time.Minute),
			FinishedAt: t0.Add(time.Duration(minute+1) * time.Minute), Outcome: outcome, FeatureCount: n, Error: msg}
	}
	if err := store.RecordIngestAttempts(ctx, []IngestAttempt{
		at("roads", "overpass", 0, IngestOK, 9993, ""),
		at("roads", "arcgis", 0, IngestOK, 5785, ""),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordIngestAttempts(ctx, []IngestAttempt{
		at("roads", "overpass", 10, IngestFailed, 0, "http2: timeout awaiting response headers"),
		at("roads", "arcgis", 10, IngestOK, 5785, ""),
		at("sidewalks", "overpass", 10, IngestEmpty, 0, ""),
	}); err != nil {
		t.Fatal(err)
	}

	hist, err := store.IngestHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 3 {
		t.Fatalf("history = %+v, want 3 keys", hist)
	}
	byKey := map[string]IngestHistory{}
	for _, h := range hist {
		byKey[h.Input+"/"+h.Source] = h
	}
	osm := byKey["roads/overpass"]
	if !osm.Failing() || osm.LastFailure == nil || osm.LastFailure.Error != "http2: timeout awaiting response headers" {
		t.Errorf("roads/overpass = %+v, want failing with the timeout", osm)
	}
	if osm.LastSuccess == nil || osm.LastSuccess.FeatureCount != 9993 || !osm.LastSuccess.FinishedAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("roads/overpass last success = %+v, want the 9993-feature run", osm.LastSuccess)
	}
	if a := byKey["roads/arcgis"]; a.Failing() || a.LastFailure != nil {
		t.Errorf("roads/arcgis = %+v, want never failed", a)
	}
	if s := byKey["sidewalks/overpass"]; s.Failing() || s.LastSuccess == nil || s.LastSuccess.Outcome != IngestEmpty {
		t.Errorf("sidewalks/overpass = %+v, want an empty answer counted as success", s)
	}
}

// TestRecordIngestAttempts_KeepsNewest: only the newest ingestAttemptsKept
// attempts per (input, source) survive, and other keys are untouched.
func TestRecordIngestAttempts_KeepsNewest(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	t0 := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	if err := store.RecordIngestAttempts(ctx, []IngestAttempt{{Input: "parking", Source: "overpass",
		StartedAt: t0, FinishedAt: t0, Outcome: IngestFailed, Error: "first"}}); err != nil {
		t.Fatal(err)
	}
	for i := range ingestAttemptsKept + 5 {
		ts := t0.Add(time.Duration(i) * time.Hour)
		if err := store.RecordIngestAttempts(ctx, []IngestAttempt{{Input: "roads", Source: "overpass",
			StartedAt: ts, FinishedAt: ts, Outcome: IngestOK, FeatureCount: i}}); err != nil {
			t.Fatal(err)
		}
	}
	s := store.(*sqliteStore)
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ingest_attempts WHERE input = 'roads'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != ingestAttemptsKept {
		t.Errorf("roads attempts kept = %d, want %d", n, ingestAttemptsKept)
	}
	hist, err := store.IngestHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hist {
		switch h.Input {
		case "roads":
			if h.Latest.FeatureCount != ingestAttemptsKept+4 {
				t.Errorf("roads latest = %+v, want the newest", h.Latest)
			}
		case "parking":
			if h.LastFailure == nil || h.LastFailure.Error != "first" {
				t.Errorf("parking = %+v, want its one attempt kept", h)
			}
		}
	}
}

// TestRecordIngestAttempts_TruncatesError: a long error is cut to the bound
// on a rune boundary.
func TestRecordIngestAttempts_TruncatesError(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	long := strings.Repeat("é", maxIngestErrorBytes) // 2 bytes per rune
	now := time.Now()
	if err := store.RecordIngestAttempts(ctx, []IngestAttempt{{Input: "roads", Source: "overpass",
		StartedAt: now, FinishedAt: now, Outcome: IngestFailed, Error: long}}); err != nil {
		t.Fatal(err)
	}
	hist, err := store.IngestHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := hist[0].LastFailure.Error
	if len(got) > maxIngestErrorBytes || !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
		t.Errorf("stored error: %d bytes, valid=%v, suffix ok=%v", len(got), utf8.ValidString(got), strings.HasSuffix(got, "…"))
	}
}

// TestIngestHistory_ScopedToCity: one city's attempts are not another's.
func TestIngestHistory_ScopedToCity(t *testing.T) {
	ctx := context.Background()
	root, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	a, err := root.EnsureCity(ctx, "a", "A", "test")
	if err != nil {
		t.Fatal(err)
	}
	b, err := root.EnsureCity(ctx, "b", "B", "test")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := root.ForCity(a).RecordIngestAttempts(ctx, []IngestAttempt{{Input: "roads", Source: "overpass",
		StartedAt: now, FinishedAt: now, Outcome: IngestOK}}); err != nil {
		t.Fatal(err)
	}
	hist, err := root.ForCity(b).IngestHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 0 {
		t.Errorf("city b history = %+v, want none", hist)
	}
}

// TestIngestAttempts_TimesReadableBySQLite: attempt times are stored as unix
// seconds, which SQLite's date functions read with 'unixepoch', and come back
// in UTC to the second.
func TestIngestAttempts_TimesReadableBySQLite(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	t0 := time.Date(2026, 11, 9, 12, 0, 0, 123e6, time.UTC)
	if err := store.RecordIngestAttempts(ctx, []IngestAttempt{{Input: "roads", Source: "overpass", StartedAt: t0, FinishedAt: t0.Add(2500 * time.Millisecond), Outcome: IngestOK}}); err != nil {
		t.Fatal(err)
	}
	var secs int
	var started string
	if err := store.(*sqliteStore).db.QueryRowContext(ctx,
		`SELECT finished_at - started_at, datetime(started_at, 'unixepoch') FROM ingest_attempts`).Scan(&secs, &started); err != nil {
		t.Fatal(err)
	}
	if secs != 2 || started != "2026-11-09 12:00:00" {
		t.Errorf("stored duration %d s, start %q; want 2 s from 2026-11-09 12:00:00", secs, started)
	}
	h, err := store.IngestHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := t0.Truncate(time.Second); !h[0].Latest.StartedAt.Equal(want) || h[0].Latest.StartedAt.Location() != time.UTC {
		t.Errorf("round trip %v, want %v in UTC", h[0].Latest.StartedAt, want)
	}
}
