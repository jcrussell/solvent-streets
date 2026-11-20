package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestWrites_WaitOutALongWriteTransaction: a write transaction another
// process holds for longer than busy_timeout (an ingest's feature upsert, a
// gc sweep) makes a compute's writes wait, not fail. Creating a run and
// saving its rows are the writes that come after hours of geometry work.
func TestWrites_WaitOutALongWriteTransaction(t *testing.T) {
	if testing.Short() {
		t.Skip("holds a write lock past the five-second busy timeout")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pvmt.db")
	root, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	id, err := root.EnsureCity(ctx, "a", "A", "test")
	if err != nil {
		t.Fatal(err)
	}
	store := root.ForCity(id)

	// The other process: a second handle on the file holding the write lock.
	other, err := sql.Open("sqlite", path+dsnPragmas)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	tx, err := other.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO cities (slug, name, config_id) VALUES ('b', 'B', 'test')`); err != nil {
		t.Fatal(err)
	}
	const hold = 6 * time.Second
	released := make(chan error, 1)
	go func() {
		time.Sleep(hold)
		released <- tx.Commit()
	}()

	start := time.Now()
	run, err := store.CreateRun(ctx, "roads", "h", map[string]string{"boundary": "d"})
	if err != nil {
		t.Fatalf("CreateRun behind a %v write transaction: %v", hold, err)
	}
	if waited := time.Since(start); waited < hold-time.Second {
		t.Fatalf("CreateRun returned after %v, before the other transaction released the lock", waited)
	}
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	if err := store.SaveHexMetrics(ctx, run.ID, []HexMetric{{HexID: "h1", Metric: "m", Value: 1}}); err != nil {
		t.Fatalf("SaveHexMetrics: %v", err)
	}
}
