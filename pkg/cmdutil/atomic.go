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
	return nil
}
