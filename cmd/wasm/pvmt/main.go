//go:build js && wasm

// Command pvmt-wasm is a WASM entry point that exposes the Go forecast
// simulation and game engine to the browser via syscall/js.
//
// Build: GOOS=js GOARCH=wasm go build -o pvmt.wasm ./cmd/wasm/pvmt
//
// All translation/simulation logic lives in the build-tag-free packages
// internal/forecast/bridge and internal/game, and the exports' calling
// convention (the error envelope, panic recovery) in internal/wasmcall; all
// three are host-tested. This file
// is only the syscall/js shim; keep it that way so the logic stays covered.
package main

import (
	"encoding/json"
	"math"
	"syscall/js"

	"github.com/jcrussell/solvent-streets/internal/forecast/bridge"
	"github.com/jcrussell/solvent-streets/internal/game"
	"github.com/jcrussell/solvent-streets/internal/wasmcall"
)

// currentGame is the single active game instance. The prototype runs one game
// at a time; gameInit replaces it.
var currentGame *game.Game

// errJSON is the {"error": ...} envelope every export in this file returns,
// so any bad JS input surfaces as JSON instead of a panic.
func errJSON(msg string) string { return wasmcall.ErrorJSON(msg) }

// stateJSON marshals the current game state (or the error envelope on failure).
func stateJSON() string {
	out, err := currentGame.StateJSON()
	if err != nil {
		return errJSON(err.Error())
	}
	return string(out)
}

func simulateForecast(args []js.Value) string {
	if len(args) < 1 {
		return errJSON("missing input argument")
	}

	out, err := bridge.Run([]byte(args[0].String()))
	if err != nil {
		return errJSON(err.Error())
	}
	return string(out)
}

// gameInit decodes the config JSON, builds a new game, and returns the initial
// StateJSON (every hex, for the first paint). On bad input it returns the error
// envelope and leaves any prior game untouched.
func gameInit(args []js.Value) string {
	if len(args) < 1 || args[0].Type() != js.TypeString {
		return errJSON("gameInit: missing config JSON argument")
	}

	var cfg game.Config
	if err := json.Unmarshal([]byte(args[0].String()), &cfg); err != nil {
		return errJSON(err.Error())
	}
	g, err := game.New(cfg)
	if err != nil {
		return errJSON(err.Error())
	}
	currentGame = g
	return stateJSON()
}

// finiteNumberArg validates that args[0] is a present, finite JS number. It
// returns ok=false for a missing/non-number/NaN/±Inf argument; the caller then
// returns a field-specific errJSON so bad JS input surfaces as an error envelope
// instead of poisoning sim state.
func finiteNumberArg(args []js.Value) (float64, bool) {
	if len(args) < 1 || args[0].Type() != js.TypeNumber {
		return 0, false
	}
	f := args[0].Float()
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// gameTick advances the sim by dt sim-years and returns the new StateJSON delta.
func gameTick(args []js.Value) string {
	if currentGame == nil {
		return errJSON("game not initialized")
	}
	dt, ok := finiteNumberArg(args)
	if !ok {
		return errJSON("gameTick: dt must be a finite number")
	}
	currentGame.Tick(dt)
	return stateJSON()
}

// gameTreatBatch applies one tier (or "auto") to many hexes in a single call —
// the paint brush sends every hex under the cursor each drag-move. args are
// (idsJSON, tier): idsJSON is a JSON string array of hex ids. One StateJSON delta
// is returned for the whole batch (TreatBatch recomputes status once).
func gameTreatBatch(args []js.Value) string {
	if currentGame == nil {
		return errJSON("game not initialized")
	}
	if len(args) < 2 || args[0].Type() != js.TypeString || args[1].Type() != js.TypeString {
		return errJSON("gameTreatBatch: expected (idsJSON, tier) string arguments")
	}
	var ids []string
	if err := json.Unmarshal([]byte(args[0].String()), &ids); err != nil {
		return errJSON("gameTreatBatch: ids must be a JSON string array: " + err.Error())
	}
	currentGame.TreatBatch(ids, args[1].String())
	return stateJSON()
}

// gameProjectInsolvency computes the macro insolvency projection for a candidate
// config WITHOUT creating a game (no board), for the pregame budget preview. It
// takes a config JSON (only cohorts/tiers/horizon/budget matter; hexes may be
// omitted) and returns {"insolvency_year": N}, or {} when the network stays
// solvent through the horizon.
func gameProjectInsolvency(args []js.Value) string {
	if len(args) < 1 || args[0].Type() != js.TypeString {
		return errJSON("gameProjectInsolvency: missing config JSON argument")
	}
	var cfg game.Config
	if err := json.Unmarshal([]byte(args[0].String()), &cfg); err != nil {
		return errJSON(err.Error())
	}
	year, ok := game.ProjectInsolvency(cfg)
	out := map[string]any{}
	if ok {
		out["insolvency_year"] = year
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// export registers f as the JS global name. A panic inside f returns the
// error envelope (wasmcall.Guard) rather than exiting the Go runtime, which
// would leave every export dead until the page reloads. onPanic, when
// non-nil, runs first.
func export(name string, f func(args []js.Value) string, onPanic func()) {
	js.Global().Set(name, js.FuncOf(func(_ js.Value, args []js.Value) any {
		return js.ValueOf(wasmcall.Guard(name, func() string { return f(args) }, onPanic))
	}))
}

// dropGame discards the game after a panic in one of its exports: its state
// may be half-updated, so later calls answer "game not initialized" until
// gameInit starts a fresh one.
func dropGame() { currentGame = nil }

func main() {
	export("simulateForecast", simulateForecast, nil)
	export("gameInit", gameInit, dropGame)
	export("gameTick", gameTick, dropGame)
	export("gameTreatBatch", gameTreatBatch, dropGame)
	export("gameProjectInsolvency", gameProjectInsolvency, nil)
	// Keep the Go runtime alive.
	select {}
}
