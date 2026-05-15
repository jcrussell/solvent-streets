package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveDistinctOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG layout is Linux-specific")
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))

	p, err := Resolve("pvmt")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	distinct := map[string]struct{}{p.Config: {}, p.Cache: {}, p.State: {}, p.Data: {}}
	if len(distinct) != 4 {
		t.Errorf("expected 4 distinct paths, got %d: %+v", len(distinct), p)
	}
}

func TestResolveNoMkdir(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG layout is Linux-specific")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))

	p, err := Resolve("pvmt")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, d := range []string{p.Config, p.Cache, p.State, p.Data} {
		if _, err := os.Stat(d); err == nil {
			t.Errorf("Resolve should not create %q -- callers do that via EnsureDir", d)
		}
	}
}

func TestEnsureDirIsIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b", "c")
	if err := EnsureDir(dir); err != nil {
		t.Fatalf("first EnsureDir: %v", err)
	}
	if err := EnsureDir(dir); err != nil {
		t.Fatalf("second EnsureDir: %v", err)
	}
}
