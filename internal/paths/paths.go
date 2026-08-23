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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	if err := validateToolName(toolName); err != nil {
		return nil, err
	}
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

// validateToolName rejects tool names that would escape or restructure the
// per-OS base directory when joined onto it. The name must be a single,
// plain path segment: non-empty, not "." or "..", containing no path
// separators (so it cannot traverse or embed subdirs) and not absolute.
// filepath.Join would otherwise silently accept "../../etc" or "/etc/x"
// and place the tool's dirs outside the intended root.
func validateToolName(toolName string) error {
	if toolName == "" {
		return errors.New("paths: tool name must not be empty")
	}
	if toolName == "." || toolName == ".." {
		return fmt.Errorf("paths: invalid tool name %q", toolName)
	}
	if strings.ContainsRune(toolName, '/') || strings.ContainsRune(toolName, filepath.Separator) {
		return fmt.Errorf("paths: tool name %q must not contain a path separator", toolName)
	}
	if filepath.IsAbs(toolName) {
		return fmt.Errorf("paths: tool name %q must not be absolute", toolName)
	}
	return nil
}

// EnsureDir is a thin wrapper around os.MkdirAll with 0o755 so callers do
// not have to remember the bit.
func EnsureDir(p string) error { return os.MkdirAll(p, 0o755) }

// HTTPCacheDir returns the directory backing the disk HTTP response cache
// (cache.CachingTransport). It is a subdirectory of Cache rather than
// Cache itself so future cache kinds get their own siblings without
// colliding with the flat `<sha256>.json` / `<sha256>.meta` entry files.
//
// Both the client factory (which creates it) and `pvmt cache prune`
// (which sweeps it) go through this accessor so the "http" literal is
// defined exactly once.
func (p *Paths) HTTPCacheDir() string { return filepath.Join(p.Cache, "http") }

// stateRoot returns the per-OS root for persistent, non-regenerable state.
// Go's stdlib has UserConfigDir and UserCacheDir but no UserStateDir.
//
// Environment overrides are taken only when absolute. A relative value would
// make Paths.State cwd-relative in violation of the type's doc contract, so
// `pvmt` run from two directories would touch two different trees. The XDG
// basedir spec says to ignore a relative value rather than error, so this
// falls through to the $HOME default (the stdlib resolvers this mirrors
// error instead; ignoring is the spec-compliant behaviour).
func stateRoot() (string, error) {
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LocalAppData"); filepath.IsAbs(d) {
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
	if d := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(d) {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state"), nil
}

// dataRoot returns the per-OS root for user-scoped data files (e.g. SQLite
// databases that should survive a cache wipe). As in stateRoot, a relative
// environment override is ignored rather than honoured: Paths.Data is joined
// with "pvmt.db" and handed to sql.Open, so a cwd-relative value silently
// forks the database per working directory.
func dataRoot() (string, error) {
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LocalAppData"); filepath.IsAbs(d) {
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
	if d := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(d) {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share"), nil
}
