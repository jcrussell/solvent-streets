package hint

import (
	"errors"
	"fmt"
	"testing"
)

// Compile-time guards: constructors must return the `error` interface, not
// *ErrHint, or a nil err comes back as a non-nil interface holding a typed
// nil (byob-errors.5).
var (
	_ func(error, string) error         = With
	_ func(error, string, ...any) error = Withf
)

func TestWith_NilErrorPropagatesAsNilInterface(t *testing.T) {
	if err := With(nil, "remediation"); err != nil {
		t.Fatalf("With(nil, ...) = %v, want nil", err)
	}
}

func TestWithf_NilErrorPropagatesAsNilInterface(t *testing.T) {
	// The whole point of returning `error` from Withf is so that this
	// comparison is true. If Withf were declared `func(...) *ErrHint`,
	// returning a typed nil would yield a non-nil interface here.
	if err := Withf(nil, "remediation"); err != nil {
		t.Fatalf("Withf(nil, ...) = %v, want nil", err)
	}
}

func TestWithf_WrapsErrorAndCarriesHint(t *testing.T) {
	base := errors.New("disk full")
	err := Withf(base, "free space in %s", "/var")
	if err == nil {
		t.Fatal("Withf returned nil for non-nil err")
	}
	if !errors.Is(err, base) {
		t.Errorf("errors.Is(err, base) = false; Unwrap chain broken")
	}
	var h *ErrHint
	if !errors.As(err, &h) {
		t.Fatalf("errors.As(*ErrHint) failed: %T", err)
	}
	if got, want := h.Hint, "free space in /var"; got != want {
		t.Errorf("Hint: got %q, want %q", got, want)
	}
	if got, want := err.Error(), "disk full"; got != want {
		t.Errorf("Error(): got %q, want %q", got, want)
	}
}

// TestWithf_SurvivesFmtErrorfWrap pins the production pattern of
// callers wrapping a hinted error with `fmt.Errorf("context: %w", err)`
// for additional context: the runner must still find the *ErrHint via
// errors.As so the hint is printed. Without %w (a bare %v wrap), the
// chain breaks and the hint is silently lost — that's the failure mode
// errors-wrap-w guards against, and this test pins the success case.
func TestWithf_SurvivesFmtErrorfWrap(t *testing.T) {
	base := errors.New("disk full")
	hinted := Withf(base, "free space in /var")
	wrapped := fmt.Errorf("compute snapshot: %w", hinted)

	var h *ErrHint
	if !errors.As(wrapped, &h) {
		t.Fatalf("errors.As(*ErrHint) through fmt.Errorf wrap failed: %T", wrapped)
	}
	if got, want := h.Hint, "free space in /var"; got != want {
		t.Errorf("Hint after wrap: got %q, want %q", got, want)
	}
	if !errors.Is(wrapped, base) {
		t.Error("errors.Is(wrapped, base) = false; wrap chain broken")
	}
}
