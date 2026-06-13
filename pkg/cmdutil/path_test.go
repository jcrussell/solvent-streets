package cmdutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeCleanDir_RefusesNonSiteDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A non-empty dir without the index.html sentinel must be refused.
	if err := os.WriteFile(filepath.Join(dir, "important.txt"), []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SafeCleanDir(dir); err == nil {
		t.Fatal("expected refusal to delete non-site directory, got nil")
	}
	if _, err := os.Stat(filepath.Join(dir, "important.txt")); err != nil {
		t.Errorf("refused clean must leave existing files intact: %v", err)
	}
}

func TestSafeCleanDir_AllowsSiteAndEmptyAndMissing(t *testing.T) {
	// Previously-generated site (has index.html) is cleaned and recreated.
	site := filepath.Join(t.TempDir(), "site")
	if err := os.MkdirAll(site, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(site, "index.html"), []byte("<html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(site, "stale.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SafeCleanDir(site); err != nil {
		t.Fatalf("site dir should be cleanable: %v", err)
	}
	entries, _ := os.ReadDir(site)
	if len(entries) != 0 {
		t.Errorf("cleaned site dir should be empty, has %d entries", len(entries))
	}

	// Empty dir and a not-yet-existing dir both succeed (and end up present).
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SafeCleanDir(empty); err != nil {
		t.Errorf("empty dir should be cleanable: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if err := SafeCleanDir(missing); err != nil {
		t.Errorf("missing dir should be created: %v", err)
	}
	if info, err := os.Stat(missing); err != nil || !info.IsDir() {
		t.Errorf("missing dir should exist after SafeCleanDir: %v", err)
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
