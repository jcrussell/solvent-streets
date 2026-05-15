package cmdutil

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestGuardPanic_NoPanicReturnsFnErr(t *testing.T) {
	want := errors.New("boom")
	var buf bytes.Buffer
	got := GuardPanic(&buf, func() error { return want })
	if !errors.Is(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if buf.Len() != 0 {
		t.Errorf("expected nothing on errOut, got %q", buf.String())
	}
}

func TestGuardPanic_RecoversPanic(t *testing.T) {
	var buf bytes.Buffer
	err := GuardPanic(&buf, func() error {
		panic("kaboom")
	})
	if err == nil {
		t.Fatal("expected error from panic, got nil")
	}
	if !strings.Contains(err.Error(), "kaboom") {
		t.Errorf("expected panic value in error, got %v", err)
	}
	if !strings.Contains(buf.String(), "kaboom") {
		t.Errorf("expected panic value in errOut, got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "goroutine") {
		t.Errorf("expected stack trace in errOut, got %q", buf.String())
	}
}

func TestGuardPanic_NilErrOutNoCrash(t *testing.T) {
	err := GuardPanic(nil, func() error {
		panic("silent")
	})
	if err == nil {
		t.Fatal("expected error from panic, got nil")
	}
}
