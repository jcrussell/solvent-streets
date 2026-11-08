# solvent-streets

[![CI](https://github.com/jcrussell/solvent-streets/actions/workflows/ci.yaml/badge.svg?branch=main)](https://github.com/jcrussell/solvent-streets/actions/workflows/ci.yaml)
[![Release](https://img.shields.io/github/v/release/jcrussell/solvent-streets?logo=github)](https://github.com/jcrussell/solvent-streets/releases)
[![Go Version](https://img.shields.io/github/go-mod/go-version/jcrussell/solvent-streets)](go.mod)
[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue.svg)](LICENSE)

Can a city afford the streets it has? `pvmt` measures how much pavement a city
maintains — roads, parking, sidewalks — from OpenStreetMap and agency GIS data,
forecasts how it deteriorates and what keeping it up costs, and publishes the
result as an interactive map and financial dashboard. It is a single pure-Go
binary with a local SQLite database, and it handles many cities side by side.

**Live showcase:** <https://joncrussell.com/solvent-streets/> — 277 US cities,
built from the configs in [`examples/`](examples/).

## Install

Download a `.deb` or a Linux/macOS tarball from the
[latest release](https://github.com/jcrussell/solvent-streets/releases/latest):

```
sudo apt install ./pvmt_<version>_linux_amd64.deb   # Debian/Ubuntu: binary, completions, manpages
tar -xzf pvmt_<version>_<os>_<arch>.tar.gz && sudo mv pvmt /usr/local/bin/   # anywhere else
```

On macOS, a tarball downloaded with a browser is quarantined, so clear it
before the first run with `xattr -d com.apple.quarantine pvmt`. To build from
source, clone the repo and run `make install`, which installs to
`~/.local/bin` (Go 1.26+; see [CONTRIBUTING.md](CONTRIBUTING.md)). `go install
github.com/jcrussell/solvent-streets/cmd/pvmt@latest` also works, but builds
without the in-browser forecast, so it can't export or serve a dashboard (see
[troubleshooting](docs/troubleshooting.md#export-errnowasm)).

## Quickstart

Create a `pvmt.toml`:

```toml
[[cities]]
name = "Alameda, CA"
overpass = true
```

Fetch the data, compute it, and look at it:

```
pvmt all ingest
pvmt all compute
pvmt serve              # http://localhost:8080
pvmt export -o ./site   # or: a static site for any web host
```

`pvmt forecast` prints the same deterioration and cost projections in the
terminal. The forecast is simulated whenever it is read, so tuning `[forecast]`
never needs a recompute.

## Where next

Every command documents itself: `pvmt help <command>`, or `man pvmt-<command>`
once the `.deb` is installed (the tarballs carry the same manpages).
[Configuration](docs/configuration.md) covers every `pvmt.toml` setting, and [examples](examples/README.md) holds real multi-city
configs to start from. When something fails, the error's hint has a matching
section in [troubleshooting](docs/troubleshooting.md); when a release needs
action from you, [upgrading](docs/upgrading.md) says what. The dashboard's
[methodology](internal/export/docs/methodology.md) explains the models, and
[validation](docs/validation.md) tests them against published city data. To
work on the code, start with [CONTRIBUTING.md](CONTRIBUTING.md) and
[architecture](docs/architecture.md).

## License

BSD-3-Clause. See [LICENSE](LICENSE).
