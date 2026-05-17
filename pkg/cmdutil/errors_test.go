package cmdutil

import (
	"errors"
	"fmt"
	"testing"
)

// Compile-time guards: constructors that can fail must return the `error`
// interface, not the concrete pointer. Returning *FlagError from a function
// signature creates the typed-nil-in-interface trap — `var err error = ctor()`
// is non-nil even when the constructor returned `(*FlagError)(nil)`. See
// byob-errors.5 for the full rationale.
var (
	_ func(string, ...any) error        = FlagErrorf
	_ func(error, string, ...any) error = Hintf
)

func TestFlagErrorf_ReturnsErrorInterface(t *testing.T) {
	err := FlagErrorf("bad flag %q", "x")
	if err == nil {
		t.Fatal("FlagErrorf returned nil")
	}
	var flagErr *FlagError
	if !errors.As(err, &flagErr) {
		t.Fatalf("errors.As(*FlagError) failed: %T", err)
	}
	if got, want := err.Error(), `bad flag "x"`; got != want {
		t.Errorf("Error(): got %q, want %q", got, want)
	}
}

func TestHintf_NilErrorPropagatesAsNilInterface(t *testing.T) {
	// The whole point of returning `error` from Hintf is so that this
	// comparison is true. If Hintf were declared `func(...) *ErrHint`,
	// returning a typed nil would yield a non-nil interface here.
	if err := Hintf(nil, "remediation"); err != nil {
		t.Fatalf("Hintf(nil, ...) = %v, want nil", err)
	}
}

func TestHintf_WrapsErrorAndCarriesHint(t *testing.T) {
	base := errors.New("disk full")
	err := Hintf(base, "free space in %s", "/var")
	if err == nil {
		t.Fatal("Hintf returned nil for non-nil err")
	}
	if !errors.Is(err, base) {
		t.Errorf("errors.Is(err, base) = false; Unwrap chain broken")
	}
	var hint *ErrHint
	if !errors.As(err, &hint) {
		t.Fatalf("errors.As(*ErrHint) failed: %T", err)
	}
	if got, want := hint.Hint, "free space in /var"; got != want {
		t.Errorf("Hint: got %q, want %q", got, want)
	}
	if got, want := err.Error(), "disk full"; got != want {
		t.Errorf("Error(): got %q, want %q", got, want)
	}
}

func TestFlagError_UnwrapAndIs(t *testing.T) {
	base := errors.New("missing required flag")
	err := error(&FlagError{Err: base})
	if !errors.Is(err, base) {
		t.Error("errors.Is(err, base) = false; Unwrap chain broken")
	}
}

// TestTypedNilTrap demonstrates the failure mode that byob-errors.5 forbids.
// A constructor that returns the concrete pointer type makes `err != nil`
// true even when the constructor returned a typed nil. The test pins the
// behavior so any future drift toward `func() *FlagError` shapes is visible
// in code review by reference.
func TestTypedNilTrap(t *testing.T) {
	// Wrong shape: returns the concrete pointer.
	bad := func(fail bool) *FlagError {
		if !fail {
			return nil
		}
		return &FlagError{Err: fmt.Errorf("boom")}
	}
	// Route through reflect-free interface assignment via a helper so
	// staticcheck doesn't constant-fold the comparison away (SA4023).
	// The trap matters at runtime, not at the call site staticcheck can see.
	err := assignToError(bad(false))
	if err == nil {
		t.Fatal("typed-nil trap absent; this test guards a Go language invariant")
	}

	// Right shape: returns the interface. A bare `return nil` is a true
	// nil interface and the caller's `err != nil` check works.
	good := func(fail bool) error {
		if !fail {
			return nil
		}
		return &FlagError{Err: fmt.Errorf("boom")}
	}
	if err := good(false); err != nil {
		t.Fatalf("good(false) = %v, want nil", err)
	}
}

// assignToError exists to hide the typed-nil pointer assignment from
// staticcheck's SA4023 constant-folding. The trap demonstrated by
// TestTypedNilTrap is a runtime property of interface assignment; we
// route through a function boundary so the analyzer cannot conclude
// the resulting interface is statically nil.
func assignToError(e *FlagError) error { return e }
