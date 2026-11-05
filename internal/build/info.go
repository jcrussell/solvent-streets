package build

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
)

type Info struct {
	Version string
	Commit  string
	Date    string
	// Modified reports vcs.modified=true: the binary was built from a tree
	// with uncommitted changes, untracked files included. Its own field,
	// never a suffix on Commit, so shortening the hash cannot cut it off.
	Modified bool
	// VCSKnown reports that the build recorded any vcs.* setting. False
	// under -buildvcs=false or outside a repository, where Modified=false
	// means "not measured", not "clean".
	VCSKnown bool
	// Sum is the main module's go.sum hash ("h1:…"), recorded only when Go
	// built the binary from a downloaded module (`go install …@v1.2.3`).
	// Such a build has no VCS data, so the sum is its provenance in place of
	// a commit — the same convention golangci-lint's --version follows.
	Sum       string
	GoVersion string
	OS        string
	Arch      string
}

// Current returns the build metadata: ldflags values are authoritative, and
// debug.ReadBuildInfo fills whatever they left at the sentinel: the module
// version (the tag for `go install …@v1.2.3`, which records no vcs.*
// settings; a pseudo-version for a plain `go build` in a checkout) and the
// commit and its time. vcs.modified is read
// regardless — the Makefile's `git describe --dirty` misses untracked files
// and a plain build sets no ldflags at all (byob-release.1). The scan runs
// once per process and never mutates the package vars.
var Current = sync.OnceValue(func() Info {
	i := Info{
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		i = fromBuildInfo(i, bi.Main.Version, bi.Settings)
		i.Sum = bi.Main.Sum
	}
	return i
})

// fromBuildInfo fills the fields ldflags left at the sentinel and always
// records the dirty state. Split from Current so tests can inject the build
// info: a test binary cannot dirty its own tree and rebuild itself.
// "(devel)" is what an unversioned build records, so it is no version.
func fromBuildInfo(i Info, mainVersion string, settings []debug.BuildSetting) Info {
	if (i.Version == VersionDev || i.Version == "") && mainVersion != "" && mainVersion != "(devel)" {
		i.Version = mainVersion
	}
	for _, s := range settings {
		if strings.HasPrefix(s.Key, "vcs.") {
			i.VCSKnown = true
		}
		switch s.Key {
		case "vcs.revision":
			if i.Commit == CommitNone || i.Commit == "" {
				i.Commit = s.Value
			}
		case "vcs.time":
			if i.Date == DateUnknown || i.Date == "" {
				i.Date = s.Value
			}
		case "vcs.modified":
			i.Modified = s.Value == "true"
		}
	}
	return i
}

// commitLabel renders the commit for display: shortened to 12 characters,
// then marked "-dirty" for a modified tree or "+unknown" when no VCS state
// was recorded. The marker goes on after shortening so truncation can never
// hide it.
func (i Info) commitLabel() string {
	c := i.Commit
	if len(c) > 12 {
		c = c[:12]
	}
	switch {
	case i.Modified:
		c += "-dirty"
	case !i.VCSKnown && i.Commit != CommitNone && i.Commit != "":
		c += "+unknown"
	}
	return c
}

// fromModule reports a binary Go built from a downloaded module: a go.sum
// hash and no commit from either ldflags or VCS.
func (i Info) fromModule() bool {
	return i.Sum != "" && (i.Commit == CommitNone || i.Commit == "")
}

// provenance is the parenthetical after the version: which commit, or for a
// module build, that it came from `go install` and which module hash.
func (i Info) provenance() string {
	if i.fromModule() {
		sum := i.Sum
		if len(sum) > 15 { // "h1:" + 12 characters of the hash
			sum = sum[:15] + "…"
		}
		return "go install, " + sum
	}
	return "commit " + i.commitLabel()
}

// Short is the --version line: "v0.4.0 (commit 81eaae7c0ffe)" for a release
// or checkout build, "v0.4.0 (go install, h1:Xq3abcdEF12g…)" for a module
// build. The version is the same either way — `go install …@v0.4.0` builds
// exactly the tagged source, checked against the checksum database — and the
// parenthetical says where the binary came from.
func (i Info) Short() string {
	return fmt.Sprintf("%s (%s)", i.Version, i.provenance())
}

func (i Info) Full() string {
	src := "  commit:  " + i.commitLabel()
	if i.fromModule() {
		src = "  source:  go install (module " + i.Sum + ")"
	}
	return fmt.Sprintf("pvmt %s\n%s\n  built:   %s\n  go:      %s\n  os/arch: %s/%s\n",
		i.Version, src, i.Date, i.GoVersion, i.OS, i.Arch)
}
