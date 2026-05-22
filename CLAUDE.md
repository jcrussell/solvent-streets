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

## Where to find more

The deep documentation lives under `docs/` — read these before changing
anything non-trivial:

- [docs/architecture.md](docs/architecture.md) — data pipeline, factory DI, geometry pipeline, database schema, design decisions (metric internals, snapshots, WASM embed, HTTP cache, Overpass splitting, forecast model).
- [docs/configuration.md](docs/configuration.md) — `pvmt.toml` discovery (walks up from cwd like `.git`), resolution hierarchy (CLI > env > per-city > top-level > default), env vars, multi-city, forecast tuning.
- [docs/troubleshooting.md](docs/troubleshooting.md) — common errors and the hints they emit.

## Gotchas

- **Pure Go only.** `CGO_ENABLED=0` is hard-required. Use `modernc.org/sqlite` for SQL and `peterstace/simplefeatures` for geometry — don't introduce CGO-backed replacements.
- **`simplefeatures` API.** `NewLineString(seq)` returns 1 value (not 2). `NewPolygon([]LineString)` takes a slice (not a Sequence). `Envelope.MinMaxXYs()` returns `(XY, XY, bool)` — use this instead of `.Min().X`. `Buffer` and `Intersection` both return `(Geometry, error)`.
