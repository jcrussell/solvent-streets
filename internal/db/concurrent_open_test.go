package db

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

// Processes that open a brand-new database at once race its creation: one
// stamps the generation and creates the tables while another reads. None may
// call the file incompatible: it is the database pvmt is creating right now.
// A raw SQLITE_BUSY from such a race is a separate, known issue and is not
// judged here.
func TestOpen_ConcurrentFirstOpenIsNeverIncompatible(t *testing.T) {
	const rounds, openers = 15, 8
	for range rounds {
		p := filepath.Join(t.TempDir(), "pvmt.db")
		var wg sync.WaitGroup
		errs := make(chan error, openers)
		for range openers {
			wg.Go(func() {
				s, err := Open(p)
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
		}
	}
}
