package cmdutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// TempPattern returns the os.CreateTemp pattern WriteFile uses for a
// given target basename. os.CreateTemp replaces the "*" with a random
// digit run, so the temp file is "<base>.tmp-<digits>".
//
// Exported because WriteFile's normal error paths clean up after
// themselves but a SIGKILL between CreateTemp and rename does not: a
// consumer that has to recognize the residue (internal/cache's pruner
// sweeps it out of the HTTP cache) must derive the shape from here rather
// than hard-coding a second copy of it that can silently drift.
func TempPattern(base string) string { return base + ".tmp-*" }

// WriteFile writes data to path atomically: temp file in the same
// directory, fsync, then rename. Implements byob-runtime-directories.3.
// If a write or rename fails, the temp file is removed so the target
// never sees a partial body and the directory accumulates no garbage.
func WriteFile(path string, data []byte, perm fs.FileMode) error {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	tmp, err := os.CreateTemp(dir, TempPattern(base))
	if err != nil {
		return fmt.Errorf("creating temp file in %q: %w", dir, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("writing %q: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("syncing %q: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("closing %q: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, perm); err != nil && !errors.Is(err, fs.ErrNotExist) {
		cleanup()
		return fmt.Errorf("chmod %q: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("renaming %q to %q: %w", tmpName, path, err)
	}
	// The body is durable (tmp.Sync above) and the rename is atomic with
	// respect to concurrent readers, but the DIRECTORY ENTRY the rename creates
	// is not durable until the directory itself is synced. On ext4 and XFS it
	// can be lost to a power failure even though WriteFile returned nil — so
	// `pvmt export` reports success, the machine loses power seconds later, and
	// meta.json comes back absent or stale. That gap is what stopped this
	// function from being as atomic as its doc claims.
	return syncDir(dir)
}

// syncDir fsyncs a directory so a rename into it survives a power failure.
//
// A failure to OPEN the directory is reported: the write itself succeeded, but
// its durability is unknown, and silently swallowing that would put this back
// where it started. A failure to SYNC is not, because some filesystems
// legitimately refuse fsync on a directory handle; on those there is nothing
// the caller could do differently, and failing the write over it would be
// worse than the risk it guards.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("opening %q to sync: %w", dir, err)
	}
	defer func() { _ = d.Close() }()
	_ = d.Sync()
	return nil
}
