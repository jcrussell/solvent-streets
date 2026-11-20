package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/internal/hint"
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

// lockNewFile creates the database file at path on a second handle, holds
// its write lock until hold passes, and returns a channel that delivers the
// release's error: another process in the middle of creating the database.
func lockNewFile(t *testing.T, path string, hold time.Duration) <-chan error {
	t.Helper()
	other, err := sql.Open("sqlite", path+dsnPragmas)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	tx, err := other.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	go func() {
		time.Sleep(hold)
		released <- tx.Rollback()
	}()
	return released
}

// TestOpen_WaitsOutAnotherProcessCreatingTheDatabase: an Open of a new file
// that another process holds the write lock on for longer than busy_timeout
// waits for it, then opens the database.
func TestOpen_WaitsOutAnotherProcessCreatingTheDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("holds a write lock past the five-second busy timeout")
	}
	path := filepath.Join(t.TempDir(), "pvmt.db")
	const hold = 6 * time.Second
	released := lockNewFile(t, path, hold)

	start := time.Now()
	root, err := Open(path)
	if err != nil {
		t.Fatalf("Open behind a %v write lock: %v", hold, err)
	}
	t.Cleanup(func() { _ = root.Close() })
	if waited := time.Since(start); waited < hold-time.Second {
		t.Fatalf("Open returned after %v, before the other process released the lock", waited)
	}
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	if _, err := root.EnsureCity(context.Background(), "a", "A", "test"); err != nil {
		t.Fatalf("the opened database is not usable: %v", err)
	}
}

// TestOpen_LockedPastTheBudgetNamesTheOtherProcess: an Open still locked out
// once its retry budget runs out fails with a hint saying another pvmt is
// initializing or writing the database.
func TestOpen_LockedPastTheBudgetNamesTheOtherProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("holds a write lock past the five-second busy timeout")
	}
	defer func(b time.Duration) { openBusyBudget = b }(openBusyBudget)
	openBusyBudget = 500 * time.Millisecond
	path := filepath.Join(t.TempDir(), "pvmt.db")
	released := lockNewFile(t, path, 8*time.Second)

	root, err := Open(path)
	if err == nil {
		_ = root.Close()
		t.Fatal("Open succeeded while another process held the write lock past its budget")
	}
	if !isBusy(err) {
		t.Errorf("err = %v, want a busy database", err)
	}
	var h *hint.ErrHint
	if !errors.As(err, &h) || !strings.Contains(h.Hint, "Another pvmt process is initializing or writing") || !strings.Contains(h.Hint, path) {
		t.Errorf("hint = %v, want one naming another pvmt process and %s", h, path)
	}
	if err := <-released; err != nil {
		t.Fatal(err)
	}
}
