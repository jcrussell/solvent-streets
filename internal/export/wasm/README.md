# Forecast WASM assets

`make wasm` writes `pvmt.wasm` (compiled from `cmd/wasm/pvmt`) and
`wasm_exec.js` (from the Go toolchain) here. Both are generated and
gitignored; this README is the one tracked file, so the directory always
exists and `//go:embed wasm` in `../template.go` compiles even when they
are absent.

That is what lets `go install github.com/jcrussell/solvent-streets/cmd/pvmt@<version>`
build: the module zip carries only tracked files, so such a binary has no
WASM. It runs every command except the ones that ship the interactive
forecast — `export`, `serve` and `check-site`'s asset check — which fail
with `export.ErrNoWasm` and a hint to use `make build` or a release.
