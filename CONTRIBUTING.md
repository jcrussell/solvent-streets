# Contributing to solvent-streets

Thanks for your interest. This guide covers the workflow for changing the
code: building and testing it, the rules for changes that alter stored
results, commit messages, writing docs, and how a release and the showcase
site go out. How the code is put together is in
[docs/architecture.md](docs/architecture.md).

## Prerequisites

You need Go at the release in the `go` line of [`go.mod`](go.mod). That line
names the exact release CI uses, and with the default `GOTOOLCHAIN=auto` the
go command downloads it for you. You also need
golangci-lint at the version in [`.golangci-version`](.golangci-version) (see
its [install guide](https://golangci-lint.run/welcome/install/#local-installation)),
`make`, and Node for the JavaScript lint and tests. Nothing needs CGO: `pvmt`
builds with `CGO_ENABLED=0`, and must keep doing so. SQL goes through
`modernc.org/sqlite` and geometry through `peterstace/simplefeatures`; don't add
CGO-backed replacements.

## Build, test, lint

`make build`, `make test` and `make lint` are the loop, and all three must be
green before a PR. `make help` lists the rest. To run one test, use
`go test -race ./internal/geo -run TestHexGrid`. To run the binary without
installing it, use `go run ./cmd/pvmt <args>`.

Always build with `make build`, not `go build ./cmd/pvmt`. The forecast WASM
(`internal/export/wasm/pvmt.wasm`) is `go:embed`ed into the binary, and
`make build` regenerates it first. A bare `go build` embeds whatever stale WASM
is on disk, or on a fresh clone none at all. That binary's `export` and
`serve` then refuse to run, and `check-site` fails its asset check
(`export.ErrNoWasm`). Unit tests use
fake assets (`exporttest.AssetFiles`), so only `integration/` needs the real
WASM, and `make test` builds it.

Unit tests never touch the network. Packages that talk to upstreams call
`offlinetest.Main` from `TestMain`, which fails the run on any non-loopback
dial. Inject a fake fetcher or use an `httptest` server instead. Live-network
tests live in `integration/` behind `make e2e`.

`make pre-commit` runs fmt, vet, lint and the JavaScript lint. Run it by hand:
git takes its hooks from `.beads/hooks`, so a `.git/hooks/pre-commit` would
never run.

Don't commit generated output. The WASM and the CLI reference (`make gendocs`:
`docs/reference/`, `manpages/`, `completions/`) are gitignored, and the release
build regenerates them.

## Changes that affect stored results

Computed results are stored per city under `Config.CityHash`
(`internal/config/hash.go`). That is a hash of the resolved settings that
change what a compute stores, plus a hand-maintained `ComputeEpoch`. Computes
update the city's latest compute under its current hash, and
`pvmt snapshots create <name>` freezes only that one. Users see this as the table in
[configuration.md](docs/configuration.md#snapshots-and-the-config-hash).
Two kinds of change have to keep that table true.

**Adding a config field.** `TestComputeInputs_EveryFieldClassified`
(`internal/config/hash_test.go`) fails until the field is entered in
`configFieldRules`. The entry needs a `mutate` func, which the test uses to
check the classification against `CityHash`. A field is *hashed* when some
producer reads it, so changing it changes a stored row. The producers are the
roads/parking/sidewalks compute, the combined pass, `services` and `census`.
For a hashed field:

- add its resolved value to `computeInputs`, with defaults, env overrides and
  `[[include]]` flattening applied;
- make it `omitempty`, with a zero value that means today's behaviour, so
  existing configs keep their hashes;
- add it to `SourceDivergence`, so the stale hint can name it.

Every other field is *presentation* (read only when exporting or rendering),
*simulated* (read only by the forecast, which is never stored), *ingest*
(decides what is fetched, not what compute does with it) or *plumbing*.
When unsure, grep the producers for the field. Hashing a field no producer
reads costs users spurious recomputes. Leaving out a field a producer does read
lets `snapshots create` freeze stale results.

**Changing compute logic.** Bump `config.ComputeEpoch` by one, in the same
commit, when a code change alters what a producer stores for the same features
and settings. Examples:

- jurisdiction classification;
- buffering or width rules;
- hex tiling, clipping or stats;
- cohort classification (`forecast.NormalizeClass`);
- routing, speeds or the services registry defaults;
- census apportionment;
- what a producer writes or drops.

Every city's latest compute under the new hash is then empty, so
`pvmt snapshots create` has nothing to freeze until the city is recomputed,
which is the point. Existing snapshots still export as created, with a
warning that they were created under other settings. Don't bump it for ingest, forecast, export, serve, CLI,
docs or test changes. Each bump costs every user a full recompute. Note the
bump in the commit body.

## Schema changes

Released migrations are immutable. `internal/db/migrations/001_init.sql` is
the v0.5.0 baseline, and every schema change is a new numbered migration in
the same directory (`002_*.sql`, `003_*.sql`, ...). `migrateFS`
(`internal/db/migrations.go`) applies the ones a database has not recorded in
`schema_version`, in numeric order, each in its own transaction. A migration
must carry an existing database forward: add tables, columns and indexes,
and backfill them in the same file. Bump the expected version in
`TestColdMigration_SchemaShape` and describe the change in
[architecture.md](docs/architecture.md#database). Rewriting a shipped
migration instead is a squash, a breaking change: it needs a
`schemaGeneration` bump and a `!` commit, and every user's database is
rebuilt. `TestReleasedMigrations_Unchanged` pins each shipped migration's
hash to the generation; add a migration's hash there in the release that
ships it.

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/). There is no
hand-maintained changelog: GoReleaser builds the release notes from commit
subjects, so write each subject for a reader of the release notes. It groups
`feat`, `fix` and `perf` and drops `docs`, `test`, `chore`, `ci`, `refactor`,
`style` and `build` (the filters are in [`.goreleaser.yaml`](.goreleaser.yaml)).
Mark a breaking change with `!` in the subject (`feat(db)!: …`). GoReleaser
matches subjects only, so a `BREAKING CHANGE:` footer alone is invisible to it.
A breaking change that needs users to act also gets steps in the next
release's section of [docs/upgrading.md](docs/upgrading.md).

To reference a bead, end the subject with its ID:
`fix(ingest): page ArcGIS by exceededTransferLimit (solvent-streets-o9a3)`.

The `.beads/hooks/commit-msg` hook checks the Conventional Commit subject (not
the bead ID). It runs when `core.hooksPath` is `.beads/hooks`, which
`bd hooks install` sets, or set it yourself with
`git config core.hooksPath .beads/hooks`. The check is
`scripts/check-commit-msg.sh`, and `make test-hooks` tests it. Dependabot
commits as `chore(deps):` and `ci(deps):`, so dependency bumps stay out of the
release notes.

## Command-line conventions

Every command follows the same rules, so the CLI reads as one tool:

- **Output streams.** A command's result goes to stdout: the rows and
  tables it prints, a `--dry-run` plan, the report of what a delete
  removed. Prose about the run goes to stderr: progress, warnings (through
  `cmdutil.Warnf`, which adds `warning: `) and hints. A command that prints
  a result offers `--json` for it: rows (`cmdutil.AddJSONFlags`) or one
  document (`cmdutil.AddDocumentJSONFlags`). `all compute` is the
  exception: its stdout is each step's report, and a script reads the
  steps' own `--json` (`pvmt <resource> compute --json`).
- **Deleting commands.** A command that deletes stored data or cached
  responses offers `--dry-run` and `-y/--yes`, goes through
  `cmdutil.ConfirmDestructive` (prompt on a TTY, refuse without `--yes`
  off one) and ends its `Long` with `cmdutil.ConfirmHelp`.
- **Messages.** Errors, warnings and hints are Go-style: no leading capital,
  no trailing period, values quoted with `%q`, commands quoted
  `'pvmt like this'`. The fix goes in a hint (`hint.Withf`), not after a
  dash, and an unknown value lists the valid ones
  (`cmdutil.UnknownValueErrorf`). `internal/msgvoice` checks these rules.
- **Exit codes.** A usage error (a bad flag, argument or `--city`) is a
  `cmdutil.FlagError` and exits 2. A command that cannot run until another
  has, or that has nothing to do, exits 3: return
  `cmdutil.NeedsFirst(err, "pvmt ...")`, or `cmdutil.SkipNeedsFirst` to warn
  and skip one city of many. Every other failure exits 1.

## Writing documentation

Each fact has one home. Other pages link to that home rather than restate
it, so a change is made once and nothing drifts:

- A command's or flag's behaviour lives in its cobra `Short`, `Long` and
  `Example` and in the flag's usage string. Other pages name the command
  and point at its `--help` (or `docs/reference/`).
- Concepts and workflows live in `docs/`. Help text gives one sentence and
  links there.
- An error's hint owns its remediation. `troubleshooting.md` explains the
  error and quotes only enough of the hint to be searchable.
- Docs describe the code as it is now.

The pages and what each owns:

| Page | Owns |
|---|---|
| [README.md](README.md) | what pvmt is, install, quickstart, where to go next |
| [CONTRIBUTING.md](CONTRIBUTING.md) | the workflow: build, test, stored-results rules, commits, docs, releases |
| [AGENTS.md](AGENTS.md) | only what is specific to coding agents |
| [docs/architecture.md](docs/architecture.md) | how the code is built and why |
| [docs/configuration.md](docs/configuration.md) | every `pvmt.toml` setting, env var and directory, and what invalidates stored results |
| [docs/troubleshooting.md](docs/troubleshooting.md) | errors, one section per hint, and silent symptoms |
| [docs/upgrading.md](docs/upgrading.md) | the steps each release needs |
| [methodology](internal/export/docs/methodology.md) | what the models mean, as shown on the site |
| [docs/validation.md](docs/validation.md) | the evidence that the models match real cities |
| [examples/](examples/README.md) | which example to start from; each config's header comment describes that config |
| `--help` (`make gendocs`) | each command's flags and usage |

Open each page with the big picture, and let the detail follow or live behind
a link. History belongs in commit messages,
release notes and `upgrading.md`, so write no "now", "no longer" or
"previously" notes, and keep no tables of superseded results. Don't list what
`ls`, `--help` or `make help` already shows. A list or table earns its place
only when it holds facts found nowhere else. When a change alters behaviour,
update the page that owns it in the same commit.

## Issue tracking

Work is tracked with [beads](https://github.com/steveyegge/beads): `bd ready`
shows what is open and `bd prime` explains the rest. GitHub Issues are welcome
too. Vulnerabilities go through [SECURITY.md](SECURITY.md), not a public issue.

## Pull requests

Keep a PR to one change, with its tests. Architectural choices are explained in
[docs/architecture.md](docs/architecture.md). Coding agents also follow
[AGENTS.md](AGENTS.md).

## Releasing and publishing the showcase

Pushing a `v*` tag releases. The release workflow first smoke-tests a snapshot
build (`--version`, `--help`, and `status` in `examples/livermore-ca`, then an
`export` there that must reach the snapshot check, which proves the forecast
WASM is embedded), so that
example's path is release-blocking; `TestShippedExamplesLoad` (`integration/`)
pins it and checks that every example still loads. GoReleaser then builds Linux and macOS
binaries (amd64 and arm64) and a `.deb`, and publishes them to GitHub Releases.
Its notes link to that release's section of
[docs/upgrading.md](docs/upgrading.md). `make release-dry-run` checks the build
locally, but not the changelog filters.

The showcase at <https://joncrussell.com/solvent-streets/> is one `pvmt export`
of [`examples/all/`](examples/all/pvmt.toml), which `[[include]]`s every other
example and stores no data of its own: an ingest or compute run from it fills
each example's data (see
[Config identity](docs/configuration.md#config-identity-config_id)). Ingest and
compute can't run in CI: the database is about 1 GB, and the upstream pulls are
slow and rate-limited. So the site is built from a developer machine. To
publish, from the repo root (the `pvmt` steps run in a subshell inside
`examples/all`, and the `make` targets run from the root):

```
(cd examples/all && pvmt all ingest && pvmt all compute)   # every city; hours (one city: --city <slug>)
(cd examples/all && pvmt snapshots create 2026-11)         # freeze every city's latest compute
make site          # render ./site/ from each city's newest snapshot
make site-report   # pvmt check-site: per-city size budgets and the asset audit
make deploy        # force-push ./site/ to gh-pages
```

`make site` never creates snapshots: publishing is the deliberate
`snapshots create`, and rolling back is `pvmt snapshots delete` of the newest
snapshot (a snapshot covers every city), which a create inside `make site`
would undo.
A city with no snapshot makes `make site` refuse, naming it. So does
incomplete data: before exporting, `make site` runs
`pvmt status --all --incomplete --exit-status` over `examples/all`, which
lists each layer that was never ingested or whose newest attempt failed,
with the command that retries it. Run those, or publish anyway with
`SITE_ALLOW_INCOMPLETE=1 make site`. Run `make site`
and `make site-report` before tagging a release that changes export output, so
a city over its size budget is caught first. GitHub Pages
serves the new commit on its next sync.
