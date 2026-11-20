package fsutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveOutputDir_RefusesWorkingDirAndAncestors(t *testing.T) {
	root := t.TempDir()
	wd := filepath.Join(root, "project", "city")
	if err := os.MkdirAll(wd, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(wd)
	for _, dir := range []string{".", "..", filepath.Join(root, "project"), root} {
		if _, err := ResolveOutputDir(dir); err == nil {
			t.Errorf("ResolveOutputDir(%q) accepted the working directory or an ancestor", dir)
		}
	}
	if _, err := ResolveOutputDir("site"); err != nil {
		t.Errorf("ResolveOutputDir(site) under the working directory: %v", err)
	}
	if _, err := ResolveOutputDir(filepath.Join(root, "elsewhere")); err != nil {
		t.Errorf("ResolveOutputDir of a sibling tree: %v", err)
	}
}

func TestResolveOutputDir_RefusesProtectedDirAndAncestors(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "configs", "city")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{cfgDir, filepath.Join(root, "configs")} {
		if _, err := ResolveOutputDir(dir, cfgDir); err == nil {
			t.Errorf("ResolveOutputDir(%q) accepted the protected directory or an ancestor", dir)
		}
	}
	if _, err := ResolveOutputDir(filepath.Join(cfgDir, "site"), cfgDir); err != nil {
		t.Errorf("a subdirectory of the config directory: %v", err)
	}
}

func TestSafePath_RejectsParentEscape(t *testing.T) {
	base := t.TempDir()
	if _, err := SafePath(base, "../etc/passwd"); err == nil {
		t.Errorf("expected error for parent-escape path, got nil")
	}
}

func TestSafePath_AcceptsContainedSubdir(t *testing.T) {
	base := t.TempDir()
	got, err := SafePath(base, "sub/dir")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(base, "sub", "dir")
	// Note: macOS adds /private symlink; we compare resolved-vs-resolved.
	wantResolved, _ := evalExistingSymlinks(want)
	if got != wantResolved {
		t.Errorf("SafePath = %q, want %q", got, wantResolved)
	}
}

func TestSafePath_AcceptsBaseItself(t *testing.T) {
	base := t.TempDir()
	if _, err := SafePath(base, "."); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSafePath_NonExistentLeafResolves(t *testing.T) {
	base := t.TempDir()
	got, err := SafePath(base, "does-not-exist-yet/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(got, base) {
		t.Errorf("SafePath = %q, want prefix %q", got, base)
	}
}

func TestSafePath_SymlinkEscapeRejected(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	linkPath := filepath.Join(base, "outlink")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if _, err := SafePath(base, "outlink/file"); err == nil {
		t.Errorf("expected error for symlink-escape path, got nil")
	}
}

func TestResolveOutputDir_RejectsRoot(t *testing.T) {
	if _, err := ResolveOutputDir("/"); err == nil {
		t.Errorf("expected error for filesystem root, got nil")
	}
}

func TestResolveOutputDir_RejectsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	if _, err := ResolveOutputDir(home); err == nil {
		t.Errorf("expected error for home directory, got nil")
	}
}

func TestResolveOutputDir_AcceptsTempSubdir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "site")
	got, err := ResolveOutputDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantResolved, _ := evalExistingSymlinks(dir)
	if got != wantResolved {
		t.Errorf("got %q, want %q", got, wantResolved)
	}
}

func TestWritableDir_ExistingWritable(t *testing.T) {
	if err := WritableDir(t.TempDir()); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWritableDir_NonExistentInWritableParent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-yet-created")
	if err := WritableDir(dir); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWritableDir_NotADirectory(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(tmpFile, nil, 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := WritableDir(tmpFile); err == nil {
		t.Errorf("expected error for non-directory, got nil")
	}
}

// TestResolveOutputDir_ComparesByIdentity: the guards compare directories by
// identity (os.SameFile), so a spelling that differs from the guard's, as
// "Site" does from "site" on a case-insensitive filesystem, is still caught.
// Linux test filesystems are case-sensitive, so this pins the identity
// helpers and a spelling Rel alone would not normalize.
func TestResolveOutputDir_ComparesByIdentity(t *testing.T) {
	root := t.TempDir()
	wd := filepath.Join(root, "project")
	if err := os.Mkdir(wd, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(wd)
	if !isAncestorOrSelf(root, wd) || !isAncestorOrSelf(wd, wd) || isAncestorOrSelf(filepath.Join(root, "x"), wd) {
		t.Error("isAncestorOrSelf misjudged an ancestor")
	}
	if !sameDir(wd, filepath.Join(root, ".", "project")) || sameDir(wd, root) {
		t.Error("sameDir misjudged a directory")
	}
	if _, err := ResolveOutputDir(wd + string(os.PathSeparator) + "."); err == nil {
		t.Error("ResolveOutputDir accepted the working directory spelled with a trailing /.")
	}
}
