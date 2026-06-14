package cmdutil

import (
	"context"
	"fmt"

	"github.com/jcrussell/solvent-streets/pkg/cmd/prompt"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

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
		return Hintf(
			FlagErrorf("%s", refusal),
			"pass --yes/-y to skip the prompt in non-interactive environments",
		)
	}
	ok, err := prompter.Confirm(ctx, question, false)
	if err != nil {
		return fmt.Errorf("confirm: %w", err)
	}
	if !ok {
		fmt.Fprintln(io.ErrOut, "Canceled.")
		return ErrCancel
	}
	return nil
}
