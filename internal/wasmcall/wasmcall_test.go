package wasmcall

import (
	"encoding/json"
	"testing"
)

func TestGuard_PassesResultThrough(t *testing.T) {
	called := false
	if got := Guard("gameTick", func() string { return `{"year":1}` }, func() { called = true }); got != `{"year":1}` || called {
		t.Errorf("Guard = %s (onPanic called: %v); want the body's result and no onPanic", got, called)
	}
}

// A panicking export returns the error envelope, and the caller keeps
// running: a second call after the panic still answers.
func TestGuard_RecoversPanicAsErrorEnvelope(t *testing.T) {
	var nilMap map[string]int
	dropped := false
	got := Guard("gameTick", func() string {
		nilMap["x"] = 1 // a runtime panic, as a nil-pointer bug would raise
		return "unreachable"
	}, func() { dropped = true })
	if !dropped {
		t.Error("onPanic was not called for a panicking body")
	}
	var env map[string]string
	if err := json.Unmarshal([]byte(got), &env); err != nil {
		t.Fatalf("Guard returned %q, not JSON: %v", got, err)
	}
	if len(env) != 1 || env["error"] == "" {
		t.Fatalf("Guard = %s; want only an error key", got)
	}
	if want := "gameTick: internal error: assignment to entry in nil map"; env["error"] != want {
		t.Errorf("error = %q; want %q", env["error"], want)
	}
	if got := Guard("gameTick", func() string { return "{}" }, nil); got != "{}" {
		t.Errorf("a call after a recovered panic = %s; want {}", got)
	}

	if got := Guard("simulateForecast", func() string { panic("boom") }, nil); got != `{"error":"simulateForecast: internal error: boom"}` {
		t.Errorf("Guard(panic string) = %s", got)
	}
}
