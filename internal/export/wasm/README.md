# Forecast WASM assets

`make wasm` compiles `cmd/wasm/pvmt` into the generated, gitignored
`pvmt.wasm` here, beside the Go toolchain's `wasm_exec.js`. This README is the
directory's one tracked file, so `//go:embed wasm` in `../assets.go` compiles
even without them. Why, and what a binary without them can't do, is in
[docs/architecture.md](../../../docs/architecture.md#design-decisions).
