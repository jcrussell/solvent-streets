//go:build js && wasm

// Command forecast-wasm is a WASM entry point that exposes the Go forecast
// simulation to the browser via syscall/js.
//
// Build: GOOS=js GOARCH=wasm go build -o forecast.wasm ./cmd/wasm/forecast
//
// All translation/simulation logic lives in the build-tag-free packages
// internal/forecast/bridge and internal/game, which are host-tested. This file
// is only the syscall/js shim; keep it that way so the logic stays covered.
package main

import (
	"encoding/json"
	"syscall/js"

	"github.com/jcrussell/solvent-streets/internal/forecast/bridge"
	"github.com/jcrussell/solvent-streets/internal/game"
)

// currentGame is the single active game instance. The prototype runs one game
// at a time; gameInit replaces it.
var currentGame *game.Game

// errJSON marshals an error message into the {"error": ...} envelope shared with
// the forecast shim, so any bad JS input surfaces as JSON instead of a panic.
func errJSON(msg string) any {
	out, _ := json.Marshal(map[string]string{"error": msg})
	return js.ValueOf(string(out))
}

// stateJSON marshals the current game state (or the error envelope on failure).
func stateJSON() any {
	out, err := currentGame.StateJSON()
	if err != nil {
		return errJSON(err.Error())
	}
	return js.ValueOf(string(out))
}

func simulateForecast(_ js.Value, args []js.Value) any {
	if len(args) < 1 {
		return js.ValueOf(`{"error":"missing input argument"}`)
	}

	out, err := bridge.Run([]byte(args[0].String()))
	if err != nil {
		return errJSON(err.Error())
	}
	return js.ValueOf(string(out))
}

// gameInit decodes the config JSON, builds a new game, and returns the initial
// StateJSON (every hex, for the first paint). On bad input it returns the error
// envelope and leaves any prior game untouched.
func gameInit(_ js.Value, args []js.Value) any {
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

// gameTick advances the sim by dt sim-years and returns the new StateJSON delta.
func gameTick(_ js.Value, args []js.Value) any {
	if currentGame == nil {
		return errJSON("game not initialized")
	}
	if len(args) < 1 || args[0].Type() != js.TypeNumber {
		return errJSON("gameTick: missing numeric dt argument")
	}
	currentGame.Tick(args[0].Float())
	return stateJSON()
}

// gameTreat applies a treatment tier to a hex and returns the new StateJSON
// delta. The returned state already reflects whether the treatment applied.
func gameTreat(_ js.Value, args []js.Value) any {
	if currentGame == nil {
		return errJSON("game not initialized")
	}
	if len(args) < 2 || args[0].Type() != js.TypeString || args[1].Type() != js.TypeString {
		return errJSON("gameTreat: expected (hexID, tier) string arguments")
	}
	currentGame.Treat(args[0].String(), args[1].String())
	return stateJSON()
}

// gameSetBudget sets the funding rate (recomputing the insolvency headline) and
// returns the new StateJSON delta.
func gameSetBudget(_ js.Value, args []js.Value) any {
	if currentGame == nil {
		return errJSON("game not initialized")
	}
	if len(args) < 1 || args[0].Type() != js.TypeNumber {
		return errJSON("gameSetBudget: missing numeric rate argument")
	}
	currentGame.SetBudget(args[0].Float())
	return stateJSON()
}

func main() {
	js.Global().Set("simulateForecast", js.FuncOf(simulateForecast))
	js.Global().Set("gameInit", js.FuncOf(gameInit))
	js.Global().Set("gameTick", js.FuncOf(gameTick))
	js.Global().Set("gameTreat", js.FuncOf(gameTreat))
	js.Global().Set("gameSetBudget", js.FuncOf(gameSetBudget))
	// Keep the Go runtime alive.
	select {}
}
