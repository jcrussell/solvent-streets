package cmdutil

import (
	"context"
	"errors"
	"fmt"

	"github.com/jcrussell/solvent-streets/internal/hint"
	"github.com/jcrussell/solvent-streets/pkg/cmd/prompt"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// yesHint is the remedy offered when a confirmation cannot be asked.
const yesHint = "pass --yes/-y to skip the prompt in non-interactive environments"

// ConfirmHelp is the help paragraph for a command that goes through
// ConfirmDestructive; verb is what it refuses to do unconfirmed ("delete").
func ConfirmHelp(verb string) string {
	return `Confirmation: prompts on TTY by default. Pass --yes/-y to skip the
prompt for non-interactive use (scripts, CI). Without --yes and
without a TTY the command refuses to ` + verb + "."
}

// ConfirmDestructive gates a destructive delete behind a user confirmation.
// When yes is true the gate is open. Otherwise: on a TTY it prompts via
// prompter and returns ErrCancel on "no"; off a TTY it refuses with a
// FlagError hinting --yes (per byob-prompter.3 — silently confirming
// because stdin isn't a TTY is exactly the failure mode the prompter
// contract is designed to prevent).
func ConfirmDestructive(ctx context.Context, io *iostreams.IOStreams, prompter prompt.Prompter, yes bool, question, refusal string) error {
	if yes {
		return nil
	}
	if !io.IsStdinTTY() {
		return hint.Withf(
			FlagErrorf("%s", refusal),
			yesHint,
		)
	}
	ok, err := prompter.Confirm(ctx, question, false)
	if errors.Is(err, prompt.ErrAborted) {
		// Ctrl+C on the confirm is a graceful cancel, same as answering
		// "no" — exit 0 via ErrCancel, not a confirm failure.
		fmt.Fprintln(io.ErrOut, "Canceled.")
		return ErrCancel
	}
	if errors.Is(err, prompt.ErrNotTTY) {
		// The prompter can refuse even when stdin passed the isatty check
		// above (e.g. TERM=dumb, where the TUI prompt can't render) —
		// give those callers the same --yes hint, not a bare error.
		return hint.Withf(
			FlagErrorf("%s", refusal),
			yesHint,
		)
	}
	if err != nil {
		return fmt.Errorf("confirm: %w", err)
	}
	if !ok {
		fmt.Fprintln(io.ErrOut, "Canceled.")
		return ErrCancel
	}
	return nil
}
