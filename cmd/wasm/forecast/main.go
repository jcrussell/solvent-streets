//go:build js && wasm

// Command forecast-wasm is a WASM entry point that exposes the Go forecast
// simulation to the browser via syscall/js.
//
// Build: GOOS=js GOARCH=wasm go build -o forecast.wasm ./cmd/wasm/forecast
//
// All translation/simulation logic lives in the build-tag-free package
// internal/forecast/bridge, which is host-tested. This file is only the
// syscall/js shim; keep it that way so the logic stays covered.
package main

import (
	"encoding/json"
	"syscall/js"

	"github.com/jcrussell/solvent-streets/internal/forecast/bridge"
)

func simulateForecast(_ js.Value, args []js.Value) any {
	if len(args) < 1 {
		return js.ValueOf(`{"error":"missing input argument"}`)
	}

	out, err := bridge.Run([]byte(args[0].String()))
	if err != nil {
		errJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
		return js.ValueOf(string(errJSON))
	}
	return js.ValueOf(string(out))
}

func main() {
	js.Global().Set("simulateForecast", js.FuncOf(simulateForecast))
	// Keep the Go runtime alive.
	select {}
}
