package db

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

// Processes that open a brand-new database at once race its creation: one
// stamps the generation, switches it to WAL and creates the tables while the
// others read. Every one must open it: none may call the file incompatible
// (it is the database pvmt is creating right now), and none may fail on the
// lock the creator holds.
func TestOpen_ConcurrentFirstOpenSucceeds(t *testing.T) {
	const rounds, openers = 15, 8
	for range rounds {
		p := filepath.Join(t.TempDir(), "pvmt.db")
		var wg sync.WaitGroup
		errs := make(chan error, openers)
		for range openers {
			wg.Go(func() {
				s, err := Open(context.Background(), p)
				if err == nil {
					err = s.Close()
				}
				errs <- err
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if errors.Is(err, ErrIncompatibleDatabase) {
				t.Fatalf("a concurrent first open called the new database incompatible: %v", err)
			}
			if err != nil {
				t.Fatalf("a concurrent first open failed: %v", err)
			}
		}
	}
}
