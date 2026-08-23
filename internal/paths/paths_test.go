package paths

import (
	"errors"
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

// TestResolveRejectsUnsafeToolName pins odsq: Resolve validates toolName so
// the join onto the per-OS base cannot escape it.
func TestResolveRejectsUnsafeToolName(t *testing.T) {
	cases := []struct {
		name     string
		toolName string
		wantErr  bool
	}{
		{name: "valid", toolName: "pvmt", wantErr: false},
		{name: "empty", toolName: "", wantErr: true},
		{name: "dotdot", toolName: "..", wantErr: true},
		{name: "traversal", toolName: "../../etc", wantErr: true},
		{name: "absolute", toolName: "/etc/x", wantErr: true},
		{name: "with separator", toolName: "a/b", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Resolve(tc.toolName)
			if tc.wantErr {
				if err == nil {
					t.Errorf("Resolve(%q) = %+v, nil; want error", tc.toolName, p)
				}
				return
			}
			if err != nil {
				t.Errorf("Resolve(%q) unexpected error: %v", tc.toolName, err)
			}
		})
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

// TestResolveIgnoresRelativeXDG pins the Paths doc contract ("all fields are
// absolute paths"). stateRoot/dataRoot are hand-rolled -- os.UserConfigDir and
// os.UserCacheDir already reject a relative override, but nothing guarded the
// two XDG vars this package reads itself. A relative XDG_DATA_HOME survived
// filepath.Join and Resolve's collision guards all the way to sql.Open, so
// `pvmt` run from two directories silently opened two different databases.
func TestResolveIgnoresRelativeXDG(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG layout is Linux-specific")
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(".local", "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(".local", "share"))

	p, err := Resolve("pvmt")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for name, d := range map[string]string{"Config": p.Config, "Cache": p.Cache, "State": p.State, "Data": p.Data} {
		if !filepath.IsAbs(d) {
			t.Errorf("%s = %q, want an absolute path", name, d)
		}
	}
	// Ignored, not honoured: the spec-compliant fallback is the $HOME default.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	if want := filepath.Join(home, ".local", "state", "pvmt"); p.State != want {
		t.Errorf("State = %q, want the $HOME fallback %q", p.State, want)
	}
	if want := filepath.Join(home, ".local", "share", "pvmt"); p.Data != want {
		t.Errorf("Data = %q, want the $HOME fallback %q", p.Data, want)
	}
}

// TestRequireAbs covers the enforcement the Windows branch depends on. It is
// tested directly rather than through Resolve because that branch is
// unreachable on the platforms CI runs -- and the bug it replaced was exactly
// an unreachable no-op that looked like a guard: an IsAbs test on LocalAppData
// fell through to os.UserCacheDir, which returns %LocalAppData% verbatim and
// errors only when it is EMPTY, handing back the identical relative value.
func TestRequireAbs(t *testing.T) {
	if _, err := requireAbs(filepath.Join(".local", "share"), nil); err == nil {
		t.Error("a relative root must be rejected, not passed through")
	}
	got, err := requireAbs(filepath.Join(t.TempDir(), "x"), nil)
	if err != nil {
		t.Errorf("an absolute root must pass: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("requireAbs returned %q, want it unchanged and absolute", got)
	}
	// An upstream error wins over the absoluteness check.
	sentinel := errors.New("boom")
	if _, err := requireAbs("", sentinel); !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want the upstream error propagated", err)
	}
}
