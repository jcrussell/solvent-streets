package build

import (
	"runtime"
	"runtime/debug"
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

// TestFromBuildInfo pins how debug.ReadBuildInfo's module version and vcs.*
// settings combine with ldflags: they fill only sentinel fields, but vcs.modified is always read —
// it describes the tree the ldflags commit came from too.
func TestFromBuildInfo(t *testing.T) {
	const rev = "0123456789abcdef0123456789abcdef01234567"
	dirty := []debug.BuildSetting{
		{Key: "vcs", Value: "git"},
		{Key: "vcs.revision", Value: rev},
		{Key: "vcs.time", Value: "2026-11-05T00:00:00Z"},
		{Key: "vcs.modified", Value: "true"},
	}

	t.Run("plain go build fills sentinels", func(t *testing.T) {
		got := fromBuildInfo(Info{Version: VersionDev, Commit: CommitNone, Date: DateUnknown}, "(devel)", dirty)
		if got.Commit != rev || got.Date != "2026-11-05T00:00:00Z" {
			t.Errorf("Commit, Date = %q, %q; want the vcs values", got.Commit, got.Date)
		}
		if !got.Modified || !got.VCSKnown {
			t.Errorf("Modified, VCSKnown = %v, %v; want true, true", got.Modified, got.VCSKnown)
		}
	})

	t.Run("ldflags win but dirty state is still read", func(t *testing.T) {
		got := fromBuildInfo(Info{Version: "v1", Commit: "abc1234", Date: "2026-01-01"}, "v9.9.9", dirty)
		if got.Version != "v1" || got.Commit != "abc1234" || got.Date != "2026-01-01" {
			t.Errorf("Version, Commit, Date = %q, %q, %q; ldflags values must win", got.Version, got.Commit, got.Date)
		}
		if !got.Modified {
			t.Error("Modified = false; vcs.modified must apply to an ldflags commit too")
		}
	})

	t.Run("go install by version records only the module version", func(t *testing.T) {
		got := fromBuildInfo(Info{Version: VersionDev, Commit: CommitNone, Date: DateUnknown}, "v0.4.0", nil)
		if got.Version != "v0.4.0" {
			t.Errorf("Version = %q; want the module version v0.4.0", got.Version)
		}
		if got.Commit != CommitNone || got.VCSKnown {
			t.Errorf("Commit, VCSKnown = %q, %v; want %q, false", got.Commit, got.VCSKnown, CommitNone)
		}
	})

	t.Run("(devel) is not a version", func(t *testing.T) {
		if got := fromBuildInfo(Info{Version: VersionDev}, "(devel)", nil); got.Version != VersionDev {
			t.Errorf("Version = %q; want %q", got.Version, VersionDev)
		}
	})

	t.Run("-buildvcs=false records nothing", func(t *testing.T) {
		got := fromBuildInfo(Info{Commit: "abc1234"}, "", []debug.BuildSetting{{Key: "-trimpath", Value: "true"}})
		if got.VCSKnown || got.Modified {
			t.Errorf("VCSKnown, Modified = %v, %v; want false, false", got.VCSKnown, got.Modified)
		}
	})
}

// TestShort_DirtyRendersDifferently pins that a dirty, a clean and an
// unmeasured build of the same commit render three different --version
// lines, and that the marker survives shortening a full 40-char hash.
func TestShort_DirtyRendersDifferently(t *testing.T) {
	const rev = "0123456789abcdef0123456789abcdef01234567"
	clean := Info{Version: "v1", Commit: rev, VCSKnown: true}.Short()
	dirty := Info{Version: "v1", Commit: rev, VCSKnown: true, Modified: true}.Short()
	unknown := Info{Version: "v1", Commit: rev}.Short()

	if clean == dirty || clean == unknown || dirty == unknown {
		t.Fatalf("renders collide:\n clean:   %q\n dirty:   %q\n unknown: %q", clean, dirty, unknown)
	}
	if want := "v1 (commit 0123456789ab)"; clean != want {
		t.Errorf("clean = %q; want %q", clean, want)
	}
	if !strings.HasSuffix(dirty, "0123456789ab-dirty)") {
		t.Errorf("dirty = %q; want the marker after the shortened hash", dirty)
	}
	if !strings.HasSuffix(unknown, "0123456789ab+unknown)") {
		t.Errorf("unknown = %q; want +unknown when no VCS state was recorded", unknown)
	}
	if got := (Info{Version: VersionDev, Commit: CommitNone}).Short(); got != "dev (commit none)" {
		t.Errorf("no-info Short() = %q; want %q", got, "dev (commit none)")
	}
}

// TestShort_ModuleBuild pins how a `go install …@v0.4.0` binary reports
// itself: the same version a release shows, with the module hash standing in
// for the commit it does not have, so it is distinguishable from a release
// without looking broken ("commit none").
func TestShort_ModuleBuild(t *testing.T) {
	const sum = "h1:Xq3abcdEF12ghijKLmnopQRstuvWXyz0123456789+/="
	mod := Info{Version: "v0.4.0", Commit: CommitNone, Date: DateUnknown, Sum: sum}
	release := Info{Version: "v0.4.0", Commit: "0123456789abcdef0123456789abcdef01234567", VCSKnown: true}

	if got, want := mod.Short(), "v0.4.0 (go install, h1:Xq3abcdEF12g…)"; got != want {
		t.Errorf("module build Short() = %q; want %q", got, want)
	}
	if got, want := release.Short(), "v0.4.0 (commit 0123456789ab)"; got != want {
		t.Errorf("release Short() = %q; want %q", got, want)
	}
	if !strings.Contains(mod.Full(), "source:  go install (module "+sum+")") {
		t.Errorf("module build Full() = %q; want the full module hash", mod.Full())
	}
	// A checkout build carries a commit, so a stray sum does not relabel it.
	if got := (Info{Version: "v1", Commit: "abc1234", VCSKnown: true, Sum: sum}).Short(); got != "v1 (commit abc1234)" {
		t.Errorf("checkout build with a sum: Short() = %q; want the commit", got)
	}
}
