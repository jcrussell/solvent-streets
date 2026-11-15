package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// IngestOutcome is how one source answered one ingest.
type IngestOutcome string

const (
	// IngestOK means the source answered with rows and they were saved.
	IngestOK IngestOutcome = "ok"
	// IngestEmpty means the source answered with no rows; stored rows were
	// kept.
	IngestEmpty IngestOutcome = "empty"
	// IngestFailed means the source did not answer, or its rows could not
	// be saved.
	IngestFailed IngestOutcome = "failed"
)

// ingestAttemptsKept is how many attempts are kept per (city, input,
// source); older ones are deleted as new ones are recorded.
const ingestAttemptsKept = 20

// attemptTimeLayout is how attempt times are stored: UTC, in a form SQLite's
// date functions read (julianday, datetime), to the millisecond.
const attemptTimeLayout = "2006-01-02 15:04:05.000Z07:00"

// maxIngestErrorBytes bounds the error text stored with a failed attempt.
const maxIngestErrorBytes = 1024

// IngestAttempt is one source's answer to one ingest of one input.
type IngestAttempt struct {
	// Input is a city_inputs key: InputBoundary or a features
	// resource_type ("roads", "facility-fire", ...).
	Input string
	// Source is the upstream asked ("overpass", "arcgis", "nominatim", ...).
	Source       string
	StartedAt    time.Time
	FinishedAt   time.Time
	Outcome      IngestOutcome
	FeatureCount int
	// Error is the failure's message, truncated; "" unless Outcome is
	// IngestFailed.
	Error string
}

// IngestHistory is what is known about one (input, source) of a city: its
// newest attempt, and its newest success and failure when it has any. A
// success is an IngestOK or IngestEmpty attempt: the source answered.
type IngestHistory struct {
	Input       string
	Source      string
	Latest      IngestAttempt
	LastSuccess *IngestAttempt
	LastFailure *IngestAttempt
}

// Failing reports whether the newest attempt failed.
func (h IngestHistory) Failing() bool { return h.Latest.Outcome == IngestFailed }

// FailingSources returns those of sources (in their order) whose newest
// attempt at input failed and whose last answer, if any, left no rows: the
// sources a city is missing for input. A failed source keeps the rows an
// earlier fetch stored (an ingest replaces only the sources that answered),
// so a source with a successful past is stale at worst, not missing.
func FailingSources(history []IngestHistory, input string, sources []string) []string {
	var out []string
	for _, src := range sources {
		for _, h := range history {
			if h.Input != input || h.Source != src {
				continue
			}
			if h.Failing() && (h.LastSuccess == nil || h.LastSuccess.FeatureCount == 0) {
				out = append(out, src)
			}
			break
		}
	}
	return out
}

// RecordIngestAttempts stores attempts for the city in one transaction, and
// deletes all but the newest ingestAttemptsKept of each (input, source) they
// touch.
func (s *sqliteStore) RecordIngestAttempts(ctx context.Context, attempts []IngestAttempt) error {
	if len(attempts) == 0 {
		return nil
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		type key struct{ input, source string }
		touched := map[key]bool{}
		for _, a := range attempts {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO ingest_attempts
					(city_id, input, source, started_at, finished_at, outcome, feature_count, error)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				s.cityID, a.Input, a.Source, a.StartedAt.UTC().Format(attemptTimeLayout), a.FinishedAt.UTC().Format(attemptTimeLayout),
				string(a.Outcome), a.FeatureCount, truncateUTF8(a.Error, maxIngestErrorBytes)); err != nil {
				return fmt.Errorf("record ingest attempt: %w", err)
			}
			touched[key{a.Input, a.Source}] = true
		}
		for k := range touched {
			if _, err := tx.ExecContext(ctx, `
				DELETE FROM ingest_attempts
				WHERE city_id = ? AND input = ? AND source = ? AND id NOT IN (
					SELECT id FROM ingest_attempts
					WHERE city_id = ? AND input = ? AND source = ?
					ORDER BY id DESC LIMIT ?)`,
				s.cityID, k.input, k.source, s.cityID, k.input, k.source, ingestAttemptsKept); err != nil {
				return fmt.Errorf("trim ingest attempts: %w", err)
			}
		}
		return nil
	})
}

// IngestHistory returns the city's history per (input, source), ordered by
// input then source. A city never ingested returns (nil, nil).
func (s *sqliteStore) IngestHistory(ctx context.Context) ([]IngestHistory, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT input, source, started_at, finished_at, outcome, feature_count, error
		FROM ingest_attempts WHERE city_id = ?
		ORDER BY input, source, id DESC`, s.cityID)
	if err != nil {
		return nil, fmt.Errorf("list ingest attempts: %w", err)
	}
	defer func() { _ = rows.Close() }() // read-only cursor

	var out []IngestHistory
	for rows.Next() {
		var a IngestAttempt
		var outcome string
		if err := rows.Scan(&a.Input, &a.Source, &a.StartedAt, &a.FinishedAt, &outcome, &a.FeatureCount, &a.Error); err != nil {
			return nil, fmt.Errorf("scan ingest attempt: %w", err)
		}
		a.Outcome = IngestOutcome(outcome)
		if n := len(out); n == 0 || out[n-1].Input != a.Input || out[n-1].Source != a.Source {
			out = append(out, IngestHistory{Input: a.Input, Source: a.Source, Latest: a})
		}
		h := &out[len(out)-1]
		switch {
		case a.Outcome == IngestFailed && h.LastFailure == nil:
			h.LastFailure = &a
		case a.Outcome != IngestFailed && h.LastSuccess == nil:
			h.LastSuccess = &a
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list ingest attempts: %w", err)
	}
	return out, nil
}

// truncateUTF8 cuts s to at most n bytes without splitting a rune, marking
// a cut with "…".
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const mark = "…"
	cut := n - len(mark)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimRightFunc(s[:cut], func(r rune) bool { return r == ' ' }) + mark
}
