package prompt

import (
	"context"
	"errors"
	"os"

	"charm.land/huh/v2"

	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// NewLive returns a Prompter backed by charm.land/huh/v2 that reads from
// io.In and renders prompts to io.ErrOut (chatter, per the iostreams
// routing contract). It refuses to prompt when stdin is not a TTY,
// returning ErrNotTTY; callers should short-circuit on --yes before
// calling Confirm rather than mapping ErrNotTTY to a fallback default —
// silently confirming because stdin happened not to be a TTY is exactly
// the failure mode byob-prompter.3 is meant to prevent.
func NewLive(io *iostreams.IOStreams) Prompter { return &live{io: io} }

type live struct{ io *iostreams.IOStreams }

// promptable gates every method. TERM=dumb is refused alongside non-TTY
// stdin because huh silently flips to its accessible mode on dumb
// terminals, and that mode ignores ctx, swallows field errors, and
// returns the default on stdin EOF — violating the Prompter contracts.
// Callers that gate destructive actions must map ErrNotTTY to their
// --yes hint themselves (cmdutil.ConfirmDestructive does), since this
// refusal can fire even when stdin passed an isatty check.
func (p *live) promptable() error {
	if !p.io.IsStdinTTY() || os.Getenv("TERM") == "dumb" {
		return ErrNotTTY
	}
	return nil
}

// run wraps a single huh field in a form wired to the prompter's
// iostreams and the caller's ctx.
func (p *live) run(ctx context.Context, f huh.Field) error {
	err := huh.NewForm(huh.NewGroup(f)).
		WithShowHelp(false).
		WithAccessible(false).                   // never take the ctx-ignoring accessible path
		WithTheme(huh.ThemeFunc(huh.ThemeBase)). // ANSI-only palette; ThemeBase ignores the isDark flag
		WithInput(p.io.In).
		WithOutput(p.io.ErrOut).
		RunWithContext(ctx)
	// tea reports external ctx cancellation as ErrProgramKilled, which
	// huh maps to ErrTimeout — check ctx first so callers see ctx.Err().
	// Deliberately unconditional: if cancellation races a submitted
	// answer, the answer is discarded — failing safe for the destructive
	// confirms this prompter gates.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, huh.ErrUserAborted) {
		return ErrAborted
	}
	return err
}

func (p *live) Confirm(ctx context.Context, msg string, def bool) (bool, error) {
	if err := p.promptable(); err != nil {
		return false, err
	}
	v := def
	field := huh.NewConfirm().
		Title(msg).
		Affirmative("Yes").
		Negative("No").
		Value(&v)
	if err := p.run(ctx, field); err != nil {
		return false, err
	}
	return v, nil
}

func (p *live) Input(ctx context.Context, msg, def string) (string, error) {
	if err := p.promptable(); err != nil {
		return "", err
	}
	// The default is prefilled and editable; plain Enter returns def,
	// matching the old "blank returns def" contract.
	v := def
	field := huh.NewInput().Title(msg).Value(&v)
	if err := p.run(ctx, field); err != nil {
		return "", err
	}
	return v, nil
}

func (p *live) Password(ctx context.Context, msg string) (string, error) {
	if err := p.promptable(); err != nil {
		return "", err
	}
	var v string
	field := huh.NewInput().
		Title(msg).
		EchoMode(huh.EchoModePassword).
		Value(&v)
	if err := p.run(ctx, field); err != nil {
		return "", err
	}
	return v, nil
}

func (p *live) Select(ctx context.Context, msg string, options []string) (int, error) {
	if err := p.promptable(); err != nil {
		return 0, err
	}
	if len(options) == 0 {
		return 0, errors.New("prompt: Select requires at least one option")
	}
	opts := make([]huh.Option[int], len(options))
	for i, o := range options {
		opts[i] = huh.NewOption(o, i)
	}
	var v int
	field := huh.NewSelect[int]().Title(msg).Options(opts...).Value(&v)
	if err := p.run(ctx, field); err != nil {
		return 0, err
	}
	return v, nil
}

func (p *live) MultiSelect(ctx context.Context, msg string, options []string) ([]int, error) {
	if err := p.promptable(); err != nil {
		return nil, err
	}
	if len(options) == 0 {
		return nil, errors.New("prompt: MultiSelect requires at least one option")
	}
	opts := make([]huh.Option[int], len(options))
	for i, o := range options {
		opts[i] = huh.NewOption(o, i)
	}
	var v []int
	field := huh.NewMultiSelect[int]().Title(msg).Options(opts...).Value(&v)
	if err := p.run(ctx, field); err != nil {
		return nil, err
	}
	return v, nil
}
