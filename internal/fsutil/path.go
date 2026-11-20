package fsutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SafePath resolves input relative to base, follows symlinks on its
// existing prefix, and rejects paths that escape base — the strict
// containment check. Use this whenever a
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
// symlinks on its existing prefix, and rejects locations no export should
// write into or clean, as a defence beside the export manifest: the filesystem root,
// the user's home directory, the working directory, and any directory that
// is or contains the working directory, the home directory or one of
// protected (the directory of the config file, say). It allows an absolute
// target outside the working directory, which SafePath does not.
func ResolveOutputDir(outputDir string, protected ...string) (string, error) {
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
	if resolved == string(os.PathSeparator) || sameDir(resolved, string(os.PathSeparator)) {
		return "", fmt.Errorf("refusing to use filesystem root %q as output directory", resolved)
	}
	type guard struct{ what, dir string }
	var guards []guard
	if home, herr := os.UserHomeDir(); herr == nil && home != "" {
		guards = append(guards, guard{"home directory", home})
	}
	if wd, werr := os.Getwd(); werr == nil {
		guards = append(guards, guard{"working directory", wd})
	}
	for _, p := range protected {
		if p != "" {
			guards = append(guards, guard{"directory", p})
		}
	}
	for _, g := range guards {
		if err := checkGuard(resolved, g.what, g.dir); err != nil {
			return "", err
		}
	}
	return resolved, nil
}

// checkGuard refuses resolved when it is guard directory dir (what names it)
// or one of dir's ancestors.
func checkGuard(resolved, what, dir string) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil //nolint:nilerr // an unresolvable guard guards nothing
	}
	if r, err := evalExistingSymlinks(dir); err == nil {
		dir = r
	}
	if !contains(resolved, dir) && !isAncestorOrSelf(resolved, dir) {
		return nil
	}
	if resolved == dir || sameDir(resolved, dir) {
		return fmt.Errorf("refusing to use %s %q as output directory", what, resolved)
	}
	return fmt.Errorf("refusing to use %q as output directory: it contains the %s %q", resolved, what, dir)
}

// contains reports whether path is dir or one of its descendants. Both are
// clean absolute paths.
func contains(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

// sameDir reports whether a and b both exist and are the same directory
// (os.SameFile), however they are spelled: on a case-insensitive
// filesystem "Site" and "site" are one directory.
func sameDir(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	return err == nil && os.SameFile(ai, bi)
}

// isAncestorOrSelf reports whether the existing directory out is dir or one
// of dir's ancestors, comparing each ancestor by identity (sameDir) rather
// than by spelling.
func isAncestorOrSelf(out, dir string) bool {
	oi, err := os.Stat(out)
	if err != nil {
		return false
	}
	for d := dir; ; d = filepath.Dir(d) {
		if di, err := os.Stat(d); err == nil && os.SameFile(oi, di) {
			return true
		}
		if filepath.Dir(d) == d {
			return false
		}
	}
}

// IsEmptyDir reports whether path is an existing directory with no entries.
func IsEmptyDir(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) == 0
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
