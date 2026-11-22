package cmdutil

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/hint"
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
	if _, ok := errors.AsType[*FlagError](err); !ok {
		t.Fatalf("got %v, want FlagError", err)
	}
	var hint *hint.ErrHint
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
	// ErrCancel exits 0 and prints no error, so the cancel is acknowledged
	// here, on stderr.
	if strings.TrimSpace(errOut.String()) == "" {
		t.Error("declining printed nothing on stderr; the cancel should be acknowledged")
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
	// Ctrl+C on the prompt is the same graceful cancel as answering no, and
	// reads the same.
	declined, _, _, declinedErr := iostreams.Test()
	declined.SetStdinTTY(true)
	_ = ConfirmDestructive(context.Background(), declined, &fakePrompter{confirmVal: false}, false, "?", "refused")
	if errOut.String() == "" || errOut.String() != declinedErr.String() {
		t.Errorf("stderr = %q, want what declining prints, %q", errOut.String(), declinedErr.String())
	}
}

func TestConfirmDestructiveNotTTYFromPrompterGetsHint(t *testing.T) {
	// The prompter can refuse even when stdin passed the isatty gate
	// (TERM=dumb) — that refusal must carry the --yes hint too, not
	// fall through to the generic confirm-failure wrap.
	io, _, _, _ := iostreams.Test()
	io.SetStdinTTY(true)
	err := ConfirmDestructive(context.Background(), io, &fakePrompter{confirmErr: prompt.ErrNotTTY}, false, "?", "refused")
	if _, ok := errors.AsType[*FlagError](err); !ok {
		t.Fatalf("got %v, want FlagError", err)
	}
	var hint *hint.ErrHint
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
