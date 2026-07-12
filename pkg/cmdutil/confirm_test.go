package cmdutil

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/pkg/cmd/prompt"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// fakePrompter returns canned Confirm results. prompt.Stub can only pop
// values, never errors, so the ErrAborted path needs its own fake.
type fakePrompter struct {
	prompt.Stub
	confirmVal bool
	confirmErr error
}

func (f *fakePrompter) Confirm(_ context.Context, _ string, _ bool) (bool, error) {
	return f.confirmVal, f.confirmErr
}

func TestConfirmDestructiveYesBypassesPrompt(t *testing.T) {
	io, _, _, _ := iostreams.Test()
	// nil prompter: any prompt attempt would panic, proving the bypass.
	if err := ConfirmDestructive(context.Background(), io, nil, true, "?", "refused"); err != nil {
		t.Fatalf("got %v, want nil", err)
	}
}

func TestConfirmDestructiveRefusesOffTTY(t *testing.T) {
	io, _, _, _ := iostreams.Test()
	err := ConfirmDestructive(context.Background(), io, nil, false, "?", "refused")
	var flagErr *FlagError
	if !errors.As(err, &flagErr) {
		t.Fatalf("got %v, want FlagError", err)
	}
	var hint *ErrHint
	if !errors.As(err, &hint) || !strings.Contains(hint.Hint, "--yes") {
		t.Errorf("got %v, want --yes hint", err)
	}
}

func TestConfirmDestructiveNoCancels(t *testing.T) {
	io, _, _, errOut := iostreams.Test()
	io.SetStdinTTY(true)
	err := ConfirmDestructive(context.Background(), io, &fakePrompter{confirmVal: false}, false, "?", "refused")
	if !errors.Is(err, ErrCancel) {
		t.Fatalf("got %v, want ErrCancel", err)
	}
	if !strings.Contains(errOut.String(), "Canceled.") {
		t.Errorf("stderr %q should contain Canceled.", errOut.String())
	}
}

func TestConfirmDestructiveYesAnswerProceeds(t *testing.T) {
	io, _, _, _ := iostreams.Test()
	io.SetStdinTTY(true)
	err := ConfirmDestructive(context.Background(), io, &fakePrompter{confirmVal: true}, false, "?", "refused")
	if err != nil {
		t.Fatalf("got %v, want nil", err)
	}
}

func TestConfirmDestructiveAbortCancels(t *testing.T) {
	io, _, _, errOut := iostreams.Test()
	io.SetStdinTTY(true)
	err := ConfirmDestructive(context.Background(), io, &fakePrompter{confirmErr: prompt.ErrAborted}, false, "?", "refused")
	if !errors.Is(err, ErrCancel) {
		t.Fatalf("got %v, want ErrCancel for aborted prompt", err)
	}
	if !strings.Contains(errOut.String(), "Canceled.") {
		t.Errorf("stderr %q should contain Canceled.", errOut.String())
	}
}

func TestConfirmDestructiveNotTTYFromPrompterGetsHint(t *testing.T) {
	// The prompter can refuse even when stdin passed the isatty gate
	// (TERM=dumb) — that refusal must carry the --yes hint too, not
	// fall through to the generic confirm-failure wrap.
	io, _, _, _ := iostreams.Test()
	io.SetStdinTTY(true)
	err := ConfirmDestructive(context.Background(), io, &fakePrompter{confirmErr: prompt.ErrNotTTY}, false, "?", "refused")
	var flagErr *FlagError
	if !errors.As(err, &flagErr) {
		t.Fatalf("got %v, want FlagError", err)
	}
	var hint *ErrHint
	if !errors.As(err, &hint) || !strings.Contains(hint.Hint, "--yes") {
		t.Errorf("got %v, want --yes hint", err)
	}
}

func TestConfirmDestructiveOtherErrorWrapped(t *testing.T) {
	io, _, _, _ := iostreams.Test()
	io.SetStdinTTY(true)
	boom := errors.New("boom")
	err := ConfirmDestructive(context.Background(), io, &fakePrompter{confirmErr: boom}, false, "?", "refused")
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "confirm:") {
		t.Fatalf("got %v, want confirm-wrapped boom", err)
	}
	if errors.Is(err, ErrCancel) {
		t.Error("unexpected ErrCancel for non-abort error")
	}
}
