# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Beads (issue tracking & memory)

This project uses **bd (beads)** for issue tracking and persistent memory.
Run `bd prime` for the full command reference.

- Use `bd` for task tracking — not TodoWrite, TaskCreate, or markdown TODO lists.
- Use `bd remember "..."` for cross-session knowledge — not MEMORY.md files.

## Remote sync — agents do NOT push or pull

Do **not** run `git push`, `git pull`, `bd dolt push`, or `bd dolt pull`
unless the user explicitly asks. The user controls when remote sync happens.
This includes session-close behavior — finish work, leave it committed
locally, and stop.

## Build & Test

```
make build    # builds WASM then CGO_ENABLED=0 binary (WASM is required — go:embed)
make test     # go test -race ./...
make lint     # golangci-lint run
make wasm     # rebuild only the forecast WASM (internal/export/wasm/)
make gendocs  # regenerate docs/reference/ from cobra
```

Run a single test: `go test -race ./internal/geo -run TestHexGrid`.
Run the binary without install: `go run ./cmd/pvmt <args>`.

**WASM build order matters.** `cmd/wasm/forecast` compiles to `internal/export/wasm/forecast.wasm` and is embedded into the main binary via `go:embed`. If you edit forecast code used by the WASM, `make build` re-runs wasm automatically; a bare `go build ./cmd/pvmt` will silently embed a stale binary.

## Architecture Overview

Pure Go (no CGO) CLI for pavement data ingestion, hex-grid coverage, and PCI decay forecasting. Single-file SQLite database, multi-city via `pvmt.toml`.

**Entry point chain.** `cmd/pvmt/main.go` → `internal/pvmtcmd/cmd.go` → `pkg/cmd/root/root.go`. Follows the `gh` CLI structure: `pkg/cmd/<name>/` per subcommand, each with `NewCmd<Name>(f *cmdutil.Factory)` and an `Options` struct with a `runF` field for test injection.

**Factory DI (`pkg/cmdutil/factory.go`).** All dependencies are lazy `sync.OnceValue`-wrapped closures on a shared `Factory`: `Config`, `HttpClient`, `RootDB`, `CurrentCity`, `CityDB`, `UnitSystem`, `IOStreams`. Commands pull only what they need. `Factory` is mutated once at construction to wire flag-aware overrides (`--city`, `--units`); it is not mutated per-request.

**Multi-city model.** `pvmt.toml` has top-level `[grid]`/`[forecast]`/`[display]` plus `[[cities]]` entries. Each city has an auto-generated slug and an independent row set in SQLite scoped by `city_id`. Resolution: CLI flag > per-city > top-level > built-in default. `Config.ResolvedForecast(city)` and `Config.ResolvedHexEdge(city)` do the merge. Commands iterate cities via `cmdutil.ForEachCity`, which builds a per-city factory and skips `ErrNoResults` silently.

**Database (`internal/db`).** `db.RootStore` opens `~/.local/share/pvmt/pvmt.db` (WAL mode). `RootStore.EnsureCity(slug, name)` → `RootStore.ForCity(id)` returns a city-scoped `db.Store`. Single consolidated migration `001_init.sql`. `snapshots` tag each compute run with a config hash for reproducibility. Tests use `dbtest.MockRootStore`; mocks must implement the full `db.Store` interface.

**Geometry pipeline (`internal/geo`).** All area math is in projected meters, never degrees. `Projector` interface with `UTMProjector` default (zone auto-picked from bbox center longitude). `ResourceType.ProcessFeatures` takes the projector explicitly. Roads/sidewalks are LineStrings buffered to polygons using widths inferred from OSM tags (`width`, `lanes`, classification defaults + parking-lane addon); parking uses polygons directly. Output is unioned, clipped to city boundary, and intersected against a flat-top hex grid via R-tree.

**Units.** All internal storage is metric (`sqm`, `$/sqm`). The `--units` flag and `[display].units` config control presentation only. `Factory.UnitSystem()` resolves in the root command's `wireUnitSystem` so the closure sees the parsed flag.

**Config discovery.** `pvmt.toml` is found by walking from the working directory upward to `/`, first match wins (like `.git`). No file → error with a `cmdutil.Hintf` remediation hint.

**HTTP cache.** `internal/cache` wraps the transport with a 24h disk cache at `~/.cache/pvmt/http/`. Ingest commands accept `--force` to bypass. Overpass auto-splits large bboxes into quadrants (depth ≤ 3) and deduplicates at boundaries.

**WASM forecast.** `cmd/wasm/forecast` builds a `js/wasm` binary embedded into `internal/export` for interactive in-browser forecasting on the exported static site and the live server (`pkg/cmd/serve`, `pkg/cmd/export`).

## Conventions & Patterns

- **Pure Go only.** `CGO_ENABLED=0` is hard-required. Use `modernc.org/sqlite` for SQL and `peterstace/simplefeatures` for geometry — don't introduce CGO-backed replacements.
- **Factory → Options → Run.** When adding a subcommand, follow the gh pattern: `NewCmd<Name>(f)` builds an `Options` struct that snapshots the accessors it will use, and `run(opts)` holds the logic. Keep `runF` injectable for tests.
- **Factory accessor snapshots.** Subcommands snapshot `f.UnitSystem`/`f.CurrentCity`/etc. at construction time (Go values are copied). Any flag-aware rebind of a factory func must happen *before* `addSubcommands` runs — see `root.wireUnitSystem` for the precedent.
- **Error hints.** User-facing errors that suggest a fix use `cmdutil.Hintf(err, "remediation text")` rather than embedding the hint in the error string. Multi-line remediation belongs in the hint, not the error value.
- **`simplefeatures` gotchas.** `NewLineString(seq)` returns 1 value (not 2). `NewPolygon([]LineString)` takes a slice (not a Sequence). `Envelope.MinMaxXYs()` returns `(XY, XY, bool)` — use this instead of `.Min().X`. `Buffer` and `Intersection` both return `(Geometry, error)`.
- **Resources.** Add a new resource type under `internal/resource/` implementing `ResourceType`; it must handle its own width/buffer logic and consume the injected `Projector`.
