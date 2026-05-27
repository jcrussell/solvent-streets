package build

import (
	"runtime"
	"strings"
	"testing"
)

// TestSentinelDefaults pins the unset-ldflags state. When the binary is
// built without `-ldflags "-X ...build.Version=..."`, the package vars
// must hold the sentinel constants so the rest of the codebase (notably
// UserAgent and `pvmt --version`) can detect "no info available" via
// constant comparison rather than raw-string matching.
func TestSentinelDefaults(t *testing.T) {
	if Version != VersionDev {
		t.Errorf("Version default = %q; want %q", Version, VersionDev)
	}
	if Commit != CommitNone {
		t.Errorf("Commit default = %q; want %q", Commit, CommitNone)
	}
	if Date != DateUnknown {
		t.Errorf("Date default = %q; want %q", Date, DateUnknown)
	}
}

// TestCurrent_PicksUpRuntime pins that Current() includes the runtime's
// Go version and GOOS/GOARCH alongside the ldflags-fed metadata. A
// regression that hardcoded these or read them at build time would
// surface as an empty GoVersion / OS / Arch in the constructed Info.
func TestCurrent_PicksUpRuntime(t *testing.T) {
	info := Current()
	if info.GoVersion == "" || !strings.HasPrefix(info.GoVersion, "go") {
		t.Errorf("GoVersion = %q; want a Go version prefix", info.GoVersion)
	}
	if info.OS != runtime.GOOS {
		t.Errorf("OS = %q; want %q", info.OS, runtime.GOOS)
	}
	if info.Arch != runtime.GOARCH {
		t.Errorf("Arch = %q; want %q", info.Arch, runtime.GOARCH)
	}
}

// TestInfo_Short_Full pin the human-facing render shapes used by the
// version command and the User-Agent fallback path.
func TestInfo_Short_Full(t *testing.T) {
	info := Info{
		Version:   "1.2.3",
		Commit:    "abcdef0",
		Date:      "2026-01-01",
		GoVersion: "go1.99",
		OS:        "linux",
		Arch:      "amd64",
	}
	short := info.Short()
	if !strings.Contains(short, "1.2.3") || !strings.Contains(short, "abcdef0") {
		t.Errorf("Short() = %q; missing version or commit", short)
	}
	full := info.Full()
	for _, want := range []string{"pvmt 1.2.3", "abcdef0", "2026-01-01", "go1.99", "linux/amd64"} {
		if !strings.Contains(full, want) {
			t.Errorf("Full() = %q; missing %q", full, want)
		}
	}
}
