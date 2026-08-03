package cmdutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SafePath resolves input relative to base, follows symlinks on its
// existing prefix, and rejects paths that escape base — the strict
// containment check from byob-input-validation.1. Use this whenever a
// user-controlled path must land inside a trusted root (archive entries,
// template includes, files served under a doc root).
//
// For CLI --output flags where absolute targets outside the cwd are
// legitimate, use ResolveOutputDir instead.
func SafePath(base, input string) (string, error) {
	if base == "" {
		return "", errors.New("base path is required")
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("resolving base: %w", err)
	}
	resolvedBase, err := evalExistingSymlinks(absBase)
	if err != nil {
		return "", fmt.Errorf("resolving base: %w", err)
	}

	joined := input
	if !filepath.IsAbs(joined) {
		joined = filepath.Join(absBase, joined)
	}
	resolved, err := evalExistingSymlinks(joined)
	if err != nil {
		return "", fmt.Errorf("resolving %q: %w", input, err)
	}

	rel, err := filepath.Rel(resolvedBase, resolved)
	if err != nil {
		return "", fmt.Errorf("checking containment of %q: %w", input, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes %q", input, base)
	}
	return resolved, nil
}

// ResolveOutputDir abs-resolves the user-supplied outputDir, follows
// symlinks on its existing prefix, and rejects sensitive locations
// (filesystem root and the user's home directory). Mirrors
// byob-input-validation.1 for the CLI output-dir case where the user
// legitimately may name absolute targets outside the cwd.
func ResolveOutputDir(outputDir string) (string, error) {
	if outputDir == "" {
		return "", errors.New("output directory is required")
	}
	abs, err := filepath.Abs(outputDir)
	if err != nil {
		return "", fmt.Errorf("resolving %q: %w", outputDir, err)
	}
	resolved, err := evalExistingSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolving %q: %w", outputDir, err)
	}
	if resolved == string(os.PathSeparator) {
		return "", fmt.Errorf("refusing to use filesystem root %q as output directory", resolved)
	}
	if home, herr := os.UserHomeDir(); herr == nil && home != "" && resolved == home {
		return "", fmt.Errorf("refusing to use home directory %q as output directory", resolved)
	}
	return resolved, nil
}

// ExportMarkerName is the sentinel file a pvmt export writes at the very start
// of a run to mark the output directory as pvmt-managed and therefore safe to
// clean. index.html is only written last, so a run that fails midway (e.g. a
// snapshot mismatch on city N of M) would otherwise leave a non-empty tree with
// no index.html that SafeCleanDir refuses to remove — dead-ending retry. The
// early marker keeps a partial export recoverable with --clean.
const ExportMarkerName = ".pvmt-export"

// SafeCleanDir removes outputDir and recreates it empty, but only if the
// directory is empty or looks like a pvmt-managed output dir — one containing
// either a completed site's index.html or the ExportMarkerName sentinel written
// at the start of a run. It refuses to delete a non-empty directory lacking
// both, so a mistyped --output cannot wipe unrelated user data. The path is
// canonicalised and screened (root/home rejected) via ResolveOutputDir first.
//
// Verifying the sentinel requires reading the directory before removing it, so
// this performs a stat/read-then-RemoveAll sequence rather than a bare
// RemoveAll: the data-loss guard is worth the small TOCTOU window for a
// single-user CLI, and matches the static-site generator's behavior.
func SafeCleanDir(outputDir string) error {
	resolved, err := ResolveOutputDir(outputDir)
	if err != nil {
		return err
	}

	info, err := os.Stat(resolved)
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(resolved, 0o755)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("output path %q exists but is not a directory", resolved)
	}

	entries, err := os.ReadDir(resolved)
	if err != nil {
		return fmt.Errorf("read output dir: %w", err)
	}
	if len(entries) > 0 {
		managed := false
		for _, e := range entries {
			if e.Name() == "index.html" || e.Name() == ExportMarkerName {
				managed = true
				break
			}
		}
		if !managed {
			return fmt.Errorf("output directory %q is non-empty and does not look like a generated site (no index.html or %s marker); refusing to delete", resolved, ExportMarkerName)
		}
	}

	if err := os.RemoveAll(resolved); err != nil {
		return fmt.Errorf("clean output dir: %w", err)
	}
	return os.MkdirAll(resolved, 0o755)
}

// WritableDir verifies path (or its nearest existing ancestor) is a
// writable directory by creating and removing a hidden temp directory
// inside it. Does not create the target path itself.
func WritableDir(path string) error {
	target := path
	for {
		info, err := os.Stat(target)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("%q is not a directory", target)
			}
			tmp, err := os.MkdirTemp(target, ".pvmt-write-probe-*")
			if err != nil {
				return fmt.Errorf("directory %q is not writable: %w", target, err)
			}
			return os.Remove(tmp)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat %q: %w", target, err)
		}
		parent := filepath.Dir(target)
		if parent == target {
			return fmt.Errorf("no existing ancestor for %q", path)
		}
		target = parent
	}
}

// evalExistingSymlinks resolves symlinks in the longest existing prefix
// of p, returning that resolved prefix joined with any non-existent
// trailing components. Plain filepath.EvalSymlinks rejects non-existent
// paths; this idiom is the documented workaround for "I'm about to
// create this path, but I want it canonicalised first."
func evalExistingSymlinks(p string) (string, error) {
	p = filepath.Clean(p)
	var trailing []string
	cur := p
	for {
		if _, err := os.Lstat(cur); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		dir, last := filepath.Split(cur)
		trailing = append([]string{last}, trailing...)
		if dir == "" {
			cur = "."
			break
		}
		next := filepath.Clean(dir)
		if next == cur {
			break
		}
		cur = next
	}
	resolved, err := filepath.EvalSymlinks(cur)
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{resolved}, trailing...)...), nil
}
