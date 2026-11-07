# Contributing to solvent-streets

Thanks for your interest. This guide covers prerequisites, the build/test
loop, commit-message style, and how this project tracks work.

## Prerequisites

- **Go 1.26+** — the floor is the `go` line in [`go.mod`](go.mod). Its
  `toolchain` line names the exact release CI builds and `make vuln` scans;
  with the default `GOTOOLCHAIN=auto`, the go command downloads it for you.
- **golangci-lint** at the version pinned in [`.golangci-version`](.golangci-version).
- A POSIX shell (`make`, `awk`, `sed`).

Linux: install Go from your distro's package manager (e.g.
`sudo apt install golang-go` on Debian/Ubuntu, `sudo dnf install golang`
on Fedora). If your distro's Go is older than 1.26, download a release
from [go.dev/dl](https://go.dev/dl/). Install `golangci-lint` via the
[install script](https://golangci-lint.run/welcome/install/#local-installation).

macOS:

```
brew install go golangci-lint
```

No CGO is required — `pvmt` builds with `CGO_ENABLED=0`. Don't introduce
CGO-backed dependencies.

## Build, test, lint

```
make build    # WASM + binary (CGO_ENABLED=0)
make test     # go test -race ./...
make lint     # golangci-lint run, pinned floor
```

Always use `make build` rather than `go build ./cmd/pvmt` directly. The
forecast WASM at `internal/export/wasm/pvmt.wasm` is `go:embed`ed
into the main binary; `make build` rebuilds the WASM first, while a bare
`go build` silently embeds whatever stale binary is on disk — or, on a fresh
clone with none, builds a pvmt whose `export`, `serve` and `check-site`
refuse to run (`export.ErrNoWasm`). Unit tests run on fake assets
(`exporttest.AssetFiles`) and pass without it; only `integration/` checks the
real WASM, and `make test` builds it first.

Run a single test: `go test -race ./internal/geo -run TestHexGrid`.

Unit tests never touch the network. Packages that talk to upstreams call
`offlinetest.Main` from `TestMain`, which refuses any non-loopback dial and
fails the run; inject a fake fetcher or use an `httptest` server instead.
Live-network tests live in `integration/` behind `make e2e`.
Run the binary without installing: `go run ./cmd/pvmt <args>`.

`make pre-commit` runs `fmt`, `vet`, `lint` and `lint-js` (which needs
Node and runs `npm ci` on first use). Run it by hand before committing. It
is not a git hook, and a `.git/hooks/pre-commit` symlink would never run:
git takes its hooks from `.beads/hooks` (`core.hooksPath`, see below).

## Changes that affect stored results

Stored results are keyed on `Config.CityHash` (`internal/config/hash.go`):
a per-city hash of the resolved settings that change what a compute
stores, plus a hand-maintained `ComputeEpoch`. A snapshot is read only
under the hash it was computed with, so two kinds of change need care.

**Adding a config field.** `TestComputeInputs_EveryFieldClassified`
(`internal/config/hash_test.go`) fails until the new field is entered in
`configFieldRules`, with a `mutate` func that the test uses to check the
classification against `CityHash`. Pick one:

- **hashed**: some producer (roads/parking/sidewalks compute, the combined
  pass, `services`, `census`) reads it, so changing it changes a stored
  row. Add its *resolved* value to `computeInputs` in `hash.go`: defaults
  applied, env overrides applied, `[[include]]` flattening respected. Make
  it `omitempty`, with a zero value that means today's behaviour, so
  configs that don't set it keep their hashes and nobody has to recompute.
  An `[[include]]`d city then hashes as its source does exactly when both
  resolve the same value; add the field to `SourceDivergence` too, so the
  stale hint can name it when they differ.
- **presentation**: read only when exporting or rendering (units, `[export]`,
  tags, sliver thresholds, legend bands).
- **simulated**: read only by the forecast, which is never stored: export,
  serve, `pvmt forecast` and the site's WASM simulate it on demand from the
  stored cohort stats and the settings in force (every `[forecast]` field).
  It stays out of the hash; an edit takes effect on the next read.
- **ingest**: decides what is fetched and stored, not what compute does with
  it (sources, URLs, boundary lookup). A re-ingest is followed by a recompute
  as usual.
- **plumbing**: identity, keys, include layout, merge stamps, presence bits.

When unsure whether a stored row depends on it, grep the producers for the
field before deciding; hashing a field no producer reads only costs users
spurious recomputes, while leaving out one that a producer reads lets
export publish stale results.

**Changing compute logic.** Bump `config.ComputeEpoch` by one, in the same
commit, when a code change alters what a producer stores for the same stored
features and the same settings: jurisdiction classification
(`internal/filter`), buffering or width rules, hex tiling, clipping or
stats, cohort classification (`forecast.NormalizeClass`, which keys
`cohort_stats`), routing, speeds or the services registry defaults, census
apportionment, or what a producer writes or drops. Every stored snapshot
then stops matching and export asks for a recompute, which is the point. Do
not bump it for changes to ingest, the forecast model or its defaults (the
forecast is simulated on demand, never stored), export or serve rendering,
CLI output, docs or tests: each bump costs every user a full recompute.
Note the bump in the commit message body.

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/). The
release changelog is generated by GoReleaser from commit subjects: it
drops `docs`, `test`, `chore`, `ci`, `refactor`, `style` and `build`
commits (scoped or not) from the release notes and groups `feat`, `fix`
and `perf` separately. See [`.goreleaser.yaml`](.goreleaser.yaml) for the
exact filters.

Mark a breaking change with `!` in the **subject** (`feat(db)!: …`,
`refactor(db)!: …`). Any type with `!` is listed under "Breaking Changes"
at the top of the release notes and is never filtered out. A
`BREAKING CHANGE:` footer alone is not enough: GoReleaser matches only
the subject. The release notes carry subjects only, never bodies, so a
breaking change that needs users to act (re-ingest, re-export, a renamed
JSON field or URL) also adds those steps to the next release's section of
[`docs/upgrading.md`](docs/upgrading.md), which the release header links.

There is no hand-maintained `CHANGELOG.md`, and PRs adding one will be
declined: the commit subject *is* the changelog entry, so write it for a
reader of the release notes.

Examples:

```
feat(forecast): add piecewise PCI decay
fix(ingest): handle 504 from Overpass with backoff
docs: link CONTRIBUTING from README
```

Scope is optional but encouraged for cross-cutting changes.

`.beads/hooks/commit-msg` enforces this shape at commit time. It runs when
`core.hooksPath` points at `.beads/hooks`, which `bd init` / `bd hooks
install` set up (or run `git config core.hooksPath .beads/hooks`). The check
itself is `scripts/check-commit-msg.sh`; `make test-hooks` runs its table
test. Dependabot is configured to commit as `chore(deps):` (Go modules) and
`ci(deps):` (Actions).

## Issue tracking

This project uses [beads](https://github.com/steveyegge/beads) (`bd`)
for issue tracking. Run `bd prime` for the full command reference.

- Browse open work: `bd ready`
- Read an issue: `bd show <id>`
- File a new issue: `bd create --title=… --description=… --type=task|bug|feature --priority=2`

GitHub Issues are also welcome. To reference a bead from a commit, end
the Conventional Commit subject with its ID:
`<type>[(<scope>)]: <summary> (<bead-id>)`, e.g.
`fix(ingest): page ArcGIS by exceededTransferLimit (solvent-streets-o9a3)`.

## Pull requests

- Keep PRs focused on a single change. Land tests in the same PR.
- Make sure `make build`, `make test`, and `make lint` are green
  locally before pushing.
- Don't commit generated output. The forecast WASM (`make wasm`) and the
  CLI reference (`make gendocs`: `docs/reference/`, `manpages/`,
  `completions/`) are gitignored. `make build` regenerates the WASM, and
  the release build regenerates both (the `before` hooks in
  [`.goreleaser.yaml`](.goreleaser.yaml)).
- For architectural choices, see [`docs/architecture.md`](docs/architecture.md)
  and the conventions in [`CLAUDE.md`](CLAUDE.md).

## Reporting security issues

Don't open public issues for vulnerabilities — see
[SECURITY.md](SECURITY.md) for private reporting channels.
