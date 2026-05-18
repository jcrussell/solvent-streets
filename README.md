# pvmt

[![CI](https://github.com/jcrussell/solvent-streets/actions/workflows/ci.yaml/badge.svg?branch=main)](https://github.com/jcrussell/solvent-streets/actions/workflows/ci.yaml)
[![Release](https://img.shields.io/github/v/release/jcrussell/solvent-streets?logo=github)](https://github.com/jcrussell/solvent-streets/releases)
[![Go Version](https://img.shields.io/github/go-mod/go-version/jcrussell/solvent-streets)](go.mod)
[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue.svg)](LICENSE)

Pure Go CLI for pavement data ingestion, hex-grid coverage analysis, PCI decay forecasting, and MapLibre visualization.

- No CGO — pure Go SQLite and geometry
- Single SQLite database, multi-city
- WASM interactive forecast in the browser
- Static site export or live server

## Install

Download a binary from [GitHub Releases](https://github.com/jcrussell/solvent-streets/releases), or build from source:

```
make build
```

## Quickstart

Create `pvmt.toml`:

```toml
[[cities]]
name = "Alameda, CA"
overpass = true
```

Live view of a single resource:

```
pvmt roads ingest
pvmt roads compute
pvmt serve
```

Open http://localhost:8080.

Full pipeline — all resources, forecast, and a deployable static site:

```
pvmt all ingest
pvmt all compute
pvmt forecast
pvmt export -o ./site
```

`./site` is a self-contained folder you can deploy to GitHub Pages or any static host.

Use `pvmt --help` and `pvmt <command> --help` for full usage.

See [`examples/`](examples/) for ready-to-use configs covering single-city, multi-city, and various US locations.

## Documentation

- [Architecture](docs/architecture.md) — data pipeline, DI, geometry, schema, design decisions
- [Configuration](docs/configuration.md) — config discovery, resolution, env vars, multi-city, forecast tuning
- [Examples](examples/) — ready-to-use configs for single-city, multi-city, and several US locations

## License

BSD-3-Clause. See [LICENSE](LICENSE).

## Development

### Prerequisites

- **Go 1.26+** (floor declared in [`go.mod`](go.mod); CI pins the same minor).
- **golangci-lint** at the version in [`.golangci-version`](.golangci-version) for `make lint`.

macOS:

```
brew install go golangci-lint
```

Linux: install Go from your distro or via [asdf](https://asdf-vm.com) / [mise](https://mise.jdx.dev). Install `golangci-lint` via the [install script](https://golangci-lint.run/welcome/install/#local-installation).

### Build

```
make build    # WASM + binary (CGO_ENABLED=0)
make test     # race detector, no external services
make lint     # golangci-lint
```

**Always use `make build`, not `go build ./cmd/pvmt` directly.** The forecast WASM at `internal/export/wasm/forecast.wasm` is `go:embed`ed into the main binary. `make build` rebuilds the WASM first; a bare `go build` silently embeds whatever stale binary is on disk.

### Release

Push a `v*` tag. GoReleaser builds Linux/macOS (amd64/arm64) and publishes to GitHub Releases.
