package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jcrussell/solvent-streets/internal/resource"
)

// InputBoundary is the input key of the city's boundary. Every other input
// key is a features resource_type, spelled exactly as resource.Type stores it
// ("roads", "facility-fire", "layer-census-blocks").
const InputBoundary = "boundary"

// Producer names that are not a resource type. A pavement compute's producer
// is its resource.Type ("roads", ...) and the combined pass's is
// resource.CombinedAll.
const (
	ProducerCensus        = "census"
	producerServicePrefix = "service:"
)

// ServiceProducer is the producer (and snapshot part) of a service's
// coverage: "service:fire".
func ServiceProducer(service string) string { return producerServicePrefix + service }

// ProducerInputs returns the inputs producer reads, as city_inputs keys, or
// nil for a producer it does not know. It is the one statement of what each
// producer depends on: producers record these digests with their run, and
// the coherence check compares them with the current ones.
//
//   - roads / parking / sidewalks: the boundary and that resource's features;
//   - combined: the boundary and every pavement resource (resource.All);
//   - service:<name>: the boundary, the roads it routes over (the city's
//     and the routing roads around it) and the service's facilities;
//   - census: the boundary and the census blocks.
func ProducerInputs(producer string) []string {
	switch {
	case producer == string(resource.CombinedAll):
		out := []string{InputBoundary}
		for _, r := range resource.All {
			out = append(out, string(r.Type()))
		}
		return out
	case producer == ProducerCensus:
		return []string{InputBoundary, string(resource.TypeCensusBlocks)}
	case strings.HasPrefix(producer, producerServicePrefix):
		service := strings.TrimPrefix(producer, producerServicePrefix)
		return []string{InputBoundary, string(resource.TypeRoads), string(resource.TypeRoutingRoads), string(resource.FacilityType(service))}
	}
	for _, r := range resource.All {
		if producer == string(r.Type()) {
			return []string{InputBoundary, producer}
		}
	}
	return nil
}

// ReadProducerInputs returns the current digest of every input producer
// reads (ProducerInputs), for the producer to record with its run
// (Draft.NewRun). Call it BEFORE reading the inputs themselves, so an ingest
// that lands in between makes the run look stale rather than current.
func ReadProducerInputs(ctx context.Context, store Store, producer string) (map[string]string, error) {
	inputs := ProducerInputs(producer)
	if inputs == nil {
		return nil, fmt.Errorf("read inputs: unknown producer %q", producer)
	}
	digests, err := store.InputDigests(ctx, inputs...)
	if err != nil {
		return nil, fmt.Errorf("read %s inputs: %w", producer, err)
	}
	return digests, nil
}

// InputState is one stored input of a city (city_inputs): the digest of its
// stored rows ("" when there are none) and when that digest last changed.
type InputState struct {
	Input      string    `json:"input"`
	Digest     string    `json:"digest"`
	IngestedAt time.Time `json:"ingested_at"`
}

// InputStates returns every input this city has stored, ordered by input.
func (s *sqliteStore) InputStates(ctx context.Context) ([]InputState, error) {
	return inputStates(ctx, s.db, s.cityID)
}

// InputDigests returns the current digest of each named input. Every name is
// in the result; one never stored maps to "".
func (s *sqliteStore) InputDigests(ctx context.Context, inputs ...string) (map[string]string, error) {
	out := make(map[string]string, len(inputs))
	if len(inputs) == 0 {
		return out, nil
	}
	for _, in := range inputs {
		out[in] = ""
	}
	args := append([]any{s.cityID}, anyArgs(inputs)...)
	// placeholders is only "?,?,..."; every name is a bound parameter.
	q := `SELECT input, digest FROM city_inputs WHERE city_id = ? AND input IN (` + placeholders(len(inputs)) + `)` //nolint:gosec // placeholders only; values are bound
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query input digests: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var in, digest string
		if err := rows.Scan(&in, &digest); err != nil {
			return nil, fmt.Errorf("scan input digest: %w", err)
		}
		out[in] = digest
	}
	return out, rows.Err()
}

// digestWriter hashes a sequence of fields unambiguously: each field is
// written as its length and then its bytes, so no two field sequences share
// an encoding.
type digestWriter struct{ h hash.Hash }

func newDigestWriter() *digestWriter { return &digestWriter{h: sha256.New()} }

func (w *digestWriter) field(s string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(s)))
	_, _ = w.h.Write(n[:])
	_, _ = w.h.Write([]byte(s))
}

func (w *digestWriter) sum() string { return hex.EncodeToString(w.h.Sum(nil)) }

// sqlFeaturesDigest reads a type's rows in id order, the primary key's order
// after its (city_id, resource_type) prefix, so no sort is needed.
const sqlFeaturesDigest = `SELECT id, source_api, name, tags, geometry, fetched_at FROM features
	WHERE city_id = ? AND resource_type = ? ORDER BY id`

// inputSummary is what city_inputs keeps of one input's stored rows.
type inputSummary struct {
	digest    string        // "" when there are no rows
	fetchedAt sql.NullInt64 // the newest fetched_at; invalid when there are no rows
	count     int           // features rows; 0 for the boundary
}

// featuresDigest summarizes this city's stored features of resourceType in
// one pass. The digest is a sha256 over every row in id order, covering what
// a producer reads (id, source_api, name, tags and the geometry) and not
// fetched_at, so re-ingesting identical data keeps the digest; "" when the
// type has no rows. The geometry enters as the WKB of its decoded
// coordinates, not the stored bytes, so a change of storage encoding that
// reads back the same coordinates keeps every digest. The same pass counts the rows and finds the newest
// fetched_at.
func featuresDigest(ctx context.Context, q queryer, cityID int64, resourceType string) (inputSummary, error) {
	var sum inputSummary
	rows, err := q.QueryContext(ctx, sqlFeaturesDigest, cityID, resourceType)
	if err != nil {
		return sum, fmt.Errorf("query %s features for digest: %w", resourceType, err)
	}
	defer func() { _ = rows.Close() }()
	w := newDigestWriter()
	for rows.Next() {
		var id, source, name, tags string
		var stored []byte
		var fetchedAt int64
		if err := rows.Scan(&id, &source, &name, &tags, &stored, &fetchedAt); err != nil {
			return sum, fmt.Errorf("scan %s feature for digest: %w", resourceType, err)
		}
		g, err := decodeGeometry(stored)
		if err != nil {
			return sum, fmt.Errorf("%s feature %s: %w", resourceType, id, err)
		}
		for _, f := range []string{id, source, name, tags, string(g.AsBinary())} {
			w.field(f)
		}
		if !sum.fetchedAt.Valid || fetchedAt > sum.fetchedAt.Int64 {
			sum.fetchedAt = sql.NullInt64{Int64: fetchedAt, Valid: true}
		}
		sum.count++
	}
	if err := rows.Err(); err != nil {
		return sum, fmt.Errorf("read %s features for digest: %w", resourceType, err)
	}
	if sum.count > 0 {
		sum.digest = w.sum()
	}
	return sum, nil
}

// boundaryDigest is the digest of a stored boundary: its geometry and source.
// It keys both the boundary's city_inputs row and its content-addressed
// snapshot_boundaries row. "" for no boundary.
func boundaryDigest(geometryJSON, source string) string {
	if geometryJSON == "" {
		return ""
	}
	w := newDigestWriter()
	w.field(geometryJSON)
	w.field(source)
	return w.sum()
}

// setInput records sum as input's current state. ingested_at moves only when
// the digest changes, so an identical re-ingest keeps it; fetched_at and
// feature_count always follow the stored rows, which an identical re-ingest
// rewrote.
func setInput(ctx context.Context, tx *sql.Tx, cityID int64, input string, sum inputSummary) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO city_inputs (city_id, input, digest, ingested_at, fetched_at, feature_count) VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(city_id, input) DO UPDATE SET
		   ingested_at = CASE WHEN city_inputs.digest != excluded.digest THEN excluded.ingested_at ELSE city_inputs.ingested_at END,
		   digest = excluded.digest,
		   fetched_at = excluded.fetched_at,
		   feature_count = excluded.feature_count`,
		cityID, input, sum.digest, nowUnix(), sum.fetchedAt, sum.count)
	if err != nil {
		return fmt.Errorf("record %s digest: %w", input, err)
	}
	return nil
}

// refreshFeaturesDigest recomputes resourceType's summary from its stored
// rows and records it.
func refreshFeaturesDigest(ctx context.Context, tx *sql.Tx, cityID int64, resourceType string) error {
	sum, err := featuresDigest(ctx, tx, cityID, resourceType)
	if err != nil {
		return err
	}
	return setInput(ctx, tx, cityID, resourceType, sum)
}

// sqlStaleFeatureTypes lists the types with rows matching where
// (staleFeatures).
func sqlStaleFeatureTypes(where string) string {
	return `SELECT DISTINCT resource_type FROM features WHERE ` + where
}

// staleFeatureTypes lists the resource types that have rows matching the
// stale-features filter, sorted, so GCSweep can refresh exactly their digests
// after deleting those rows.
func staleFeatureTypes(ctx context.Context, tx *sql.Tx, where string, args []any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, sqlStaleFeatureTypes(where), args...)
	if err != nil {
		return nil, fmt.Errorf("query stale feature types: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("scan stale feature type: %w", err)
		}
		out = append(out, t)
	}
	slices.Sort(out)
	return out, rows.Err()
}

// freezeBoundary returns the id of the snapshot_boundaries row holding this
// city's current boundary, inserting it if no snapshot has frozen that
// boundary yet. ok is false when the city has no stored boundary.
func freezeBoundary(ctx context.Context, tx *sql.Tx, cityID int64) (id int64, ok bool, err error) {
	var geometry, source string
	err = tx.QueryRowContext(ctx, `SELECT geometry_json, source FROM city_boundaries WHERE city_id = ?`, cityID).
		Scan(&geometry, &source)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read boundary to freeze: %w", err)
	}
	digest := boundaryDigest(geometry, source)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO snapshot_boundaries (city_id, digest, geometry_json, source) VALUES (?, ?, ?, ?)
		 ON CONFLICT(city_id, digest) DO NOTHING`, cityID, digest, geometry, source); err != nil {
		return 0, false, fmt.Errorf("freeze boundary: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM snapshot_boundaries WHERE city_id = ? AND digest = ?`,
		cityID, digest).Scan(&id); err != nil {
		return 0, false, fmt.Errorf("look up frozen boundary: %w", err)
	}
	return id, true, nil
}

// sweepSnapshotBoundaries deletes this city's frozen boundaries that no
// snapshot references any more.
func sweepSnapshotBoundaries(ctx context.Context, tx *sql.Tx, cityID int64) error {
	if _, err := tx.ExecContext(ctx, sqlSweepSnapshotBoundaries, cityID); err != nil {
		return fmt.Errorf("delete unreferenced snapshot boundaries: %w", err)
	}
	return nil
}

// recordRunInputs writes the digests a run read, in one transaction with the
// run itself.
func recordRunInputs(ctx context.Context, tx *sql.Tx, runID int64, inputs map[string]string) error {
	for _, in := range slices.Sorted(maps.Keys(inputs)) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_inputs (run_id, input, digest) VALUES (?, ?, ?)`,
			runID, in, inputs[in]); err != nil {
			return fmt.Errorf("record run input %s: %w", in, err)
		}
	}
	return nil
}
