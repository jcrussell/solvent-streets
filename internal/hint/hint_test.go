package hint

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Compile-time guards: constructors must return the `error` interface, not
// *ErrHint, or a nil err comes back as a non-nil interface holding a typed
// nil.
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

func TestFprint(t *testing.T) {
	var b strings.Builder
	Fprint(&b, fmt.Errorf("wrapped: %w", With(errors.New("boom"), "fix line one\nthen line two")))
	if got, want := b.String(), "hint: fix line one\n      then line two\n"; got != want {
		t.Errorf("Fprint = %q, want %q", got, want)
	}
	b.Reset()
	Fprint(&b, errors.New("no hint"))
	if b.Len() != 0 {
		t.Errorf("an error without a hint printed %q", b.String())
	}
}

// TestHints_EveryBranch: a join yields each branch's outermost hint, in
// order and without repeats; a chain yields only its outermost.
func TestHints_EveryBranch(t *testing.T) {
	inner := With(errors.New("inner"), "inner hint")
	err := fmt.Errorf("run: %w", errors.Join(
		With(errors.New("a"), "first"),
		errors.New("no hint"),
		With(inner, "outer"),
		fmt.Errorf("wrapped: %w", With(errors.New("c"), "first")),
	))
	got := Hints(err)
	if want := []string{"first", "outer"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Hints = %q, want %q", got, want)
	}
	if Hints(errors.New("plain")) != nil {
		t.Error("an error without a hint has hints")
	}
}

// TestAddf_KeepsTheCarriedHint: a hint added to an error that carries one
// prints after it rather than hiding it.
func TestAddf_KeepsTheCarriedHint(t *testing.T) {
	if Addf(nil, "x") != nil {
		t.Error("Addf(nil) is not nil")
	}
	if got := Hints(Addf(errors.New("a"), "retry %d", 1)); fmt.Sprint(got) != "[retry 1]" {
		t.Errorf("no carried hint: %q", got)
	}
	err := Addf(With(errors.New("a"), "cause"), "retry")
	var b strings.Builder
	Fprint(&b, err)
	if want := "hint: cause\n      retry\n"; b.String() != want {
		t.Errorf("printed %q, want %q", b.String(), want)
	}
	b.Reset()
	Fprint(&b, Addf(errors.Join(With(errors.New("a"), "first"), With(errors.New("b"), "second")), "retry"))
	if want := "hint: first\n      second\n      retry\n"; b.String() != want {
		t.Errorf("join: printed %q, want %q", b.String(), want)
	}
}
