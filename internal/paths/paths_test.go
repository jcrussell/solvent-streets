package paths

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// TestResolveRejectsRelativeHome: os.UserHomeDir
// returns $HOME verbatim on Unix and errors only when it is empty, so a
// relative $HOME used to yield a cwd-relative Paths.State / Paths.Data (and
// Config / Cache, via the stdlib resolvers) with no error. Unlike a relative
// XDG var there is no fallback to ignore it into, so Resolve must error.
func TestResolveRejectsRelativeHome(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG layout is Linux-specific")
	}
	for _, v := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME"} {
		t.Setenv(v, "")
	}
	t.Setenv("HOME", "somewhere")
	p, err := Resolve("pvmt")
	if err == nil {
		t.Fatalf("Resolve with a relative $HOME = %+v, want an error", p)
	}
	// The error names the variable to fix, not only the root derived from it.
	if !strings.Contains(err.Error(), "$HOME") {
		t.Errorf("err = %v, want it to name $HOME", err)
	}
	// Absolute XDG vars for every root mean $HOME is never consulted, so a
	// relative $HOME is then harmless and must not error.
	for _, v := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME"} {
		t.Setenv(v, filepath.Join(t.TempDir(), v))
	}
	if _, err := Resolve("pvmt"); err != nil {
		t.Errorf("Resolve with absolute XDG vars and a relative $HOME: %v", err)
	}
}

// TestRequireAbs covers the enforcement the Windows branch depends on. It is
// tested directly rather than through Resolve because that branch is
// unreachable on the platforms CI runs -- and the bug it replaced was exactly
// an unreachable no-op that looked like a guard: an IsAbs test on LocalAppData
// fell through to os.UserCacheDir, which returns %LocalAppData% verbatim and
// errors only when it is EMPTY, handing back the identical relative value.
func TestRequireAbs(t *testing.T) {
	if _, err := requireAbs("$HOME", rootOf(filepath.Join(".local", "share"), nil)); err == nil {
		t.Error("a relative root must be rejected, not passed through")
	}
	got, err := requireAbs("$HOME", rootOf(filepath.Join(t.TempDir(), "x"), nil))
	if err != nil {
		t.Errorf("an absolute root must pass: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("requireAbs returned %q, want it unchanged and absolute", got)
	}
	// An upstream error wins over the absoluteness check.
	sentinel := errors.New("boom")
	if _, err := requireAbs("$HOME", rootOf("", sentinel)); !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want the upstream error propagated", err)
	}
}

// rootOf is a stub stdlib resolver returning dir and err.
func rootOf(dir string, err error) func() (string, error) {
	return func() (string, error) { return dir, err }
}

// TestEnsureDir_OwnerOnly: EnsureDir creates a directory owner-only, and
// NarrowDir narrows an existing one that lets others in, leaving its parent
// alone.
func TestEnsureDir_OwnerOnly(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(parent, "pvmt")
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(p); err != nil {
		t.Fatal(err)
	}
	if err := NarrowDir(p); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(parent, "new", "pvmt")
	if err := EnsureDir(fresh); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{p, fresh} {
		if info, _ := os.Stat(d); info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s mode = %v, want owner-only", d, info.Mode().Perm())
		}
	}
	if info, _ := os.Stat(parent); info.Mode().Perm() != 0o755 {
		t.Errorf("EnsureDir changed the parent's mode to %v", info.Mode().Perm())
	}
}
