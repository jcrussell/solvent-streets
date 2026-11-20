// Package wasmcall holds the build-tag-free half of the WASM exports'
// calling convention (cmd/wasm/pvmt): every export returns a JSON string,
// and a failure is the {"error": "<message>"} envelope. Guard keeps a panic
// inside an export from exiting the Go runtime, which would leave every
// export dead until the page reloads.
package wasmcall

import (
	"encoding/json"
	"fmt"
)

// ErrorJSON is the {"error": msg} envelope.
func ErrorJSON(msg string) string {
	out, _ := json.Marshal(map[string]string{"error": msg})
	return string(out)
}

// Guard runs the export name's body f and returns what it returns. When f
// panics it calls onPanic (when non-nil), so the caller can drop state the
// panic may have left half-updated, and returns the error envelope instead,
// naming the export and the panic value.
func Guard(name string, f func() string, onPanic func()) (out string) {
	defer func() {
		if r := recover(); r != nil {
			if onPanic != nil {
				onPanic()
			}
			out = ErrorJSON(fmt.Sprintf("%s: internal error: %v", name, r))
		}
	}()
	return f()
}
