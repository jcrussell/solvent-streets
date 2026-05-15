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
