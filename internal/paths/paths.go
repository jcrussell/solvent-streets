// Package paths resolves the per-OS user-scoped directories that pvmt
// reads and writes outside the project tree: Config, Cache, State, Data.
// Resolution follows byob-runtime-directories.1's "four-directory model":
// stdlib UserConfigDir/UserCacheDir, plus hand-rolled State and Data
// resolvers because Go's stdlib does not expose them.
//
// First-use mkdir is intentionally NOT done in Resolve so that --version
// and --help pay no filesystem cost. Callers that are about to write call
// EnsureDir(p) first (byob-runtime-directories.2).
package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

// Paths holds the four resolved per-user, per-tool directories. All fields
// are absolute paths joined with the tool name (e.g. ".../pvmt").
type Paths struct {
	Config string
	Cache  string
	State  string
	Data   string
}

// Resolve returns the four user-scoped directories for the named tool,
// each joined with toolName. State and Data roots can collide with Config
// or Cache on macOS and Windows; when they do, the colliding directory is
// nested under a dedicated subdir so all four are always distinct.
func Resolve(toolName string) (*Paths, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	state, err := stateRoot()
	if err != nil {
		return nil, err
	}
	data, err := dataRoot()
	if err != nil {
		return nil, err
	}

	p := &Paths{
		Config: filepath.Join(cfg, toolName),
		Cache:  filepath.Join(cache, toolName),
		State:  filepath.Join(state, toolName),
		Data:   filepath.Join(data, toolName),
	}
	if p.State == p.Config || p.State == p.Cache {
		p.State = filepath.Join(p.State, "state")
	}
	if p.Data == p.Config || p.Data == p.Cache || p.Data == p.State {
		p.Data = filepath.Join(p.Data, "data")
	}
	return p, nil
}

// EnsureDir is a thin wrapper around os.MkdirAll with 0o755 so callers do
// not have to remember the bit.
func EnsureDir(p string) error { return os.MkdirAll(p, 0o755) }

// stateRoot returns the per-OS root for persistent, non-regenerable state.
// Go's stdlib has UserConfigDir and UserCacheDir but no UserStateDir.
func stateRoot() (string, error) {
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LocalAppData"); d != "" {
			return d, nil
		}
		return os.UserCacheDir()
	}
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support"), nil
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state"), nil
}

// dataRoot returns the per-OS root for user-scoped data files (e.g. SQLite
// databases that should survive a cache wipe).
func dataRoot() (string, error) {
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LocalAppData"); d != "" {
			return d, nil
		}
		return os.UserCacheDir()
	}
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support"), nil
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share"), nil
}
