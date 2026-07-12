// Package prompt defines the narrow Prompter interface that destructive or
// otherwise-interactive commands depend on. Per byob-prompter.1 the
// interface is intentionally small (five methods) so swapping the live
// implementation touches one file rather than every caller. The live impl
// (live.go) is backed by charm.land/huh/v2 — the original objection to huh
// (v1.x rode the old github.com/charmbracelet dep tree and would have
// forked the repo's charm.land bubbletea/v2 stack) evaporated when huh v2
// shipped on the same charm.land tree. Behavior changes versus the old
// stdlib impl are accepted: stdin EOF mid-prompt no longer surfaces
// io.EOF (the form blocks until ctx cancellation); the answered prompt
// is cleared from scrollback when the form quits; and Input prefills
// the default as editable text (typed input edits it rather than
// replacing it, and clearing the field submits ""), where the old impl
// returned the default only on a blank line.
package prompt

import (
	"context"
	"errors"
)

// ErrNotTTY is returned by Live methods when stdin is not a terminal.
// Callers that have a --yes flag should short-circuit before consulting
// the Prompter; callers without one should map this to a clear error
// (per byob-prompter.3: "pass --yes to skip confirmation in
// non-interactive environments").
var ErrNotTTY = errors.New("no TTY available for prompting")

// ErrAborted is returned when the user aborts a prompt (Ctrl+C). Under
// the live impl's raw terminal mode Ctrl+C is a key event that aborts
// the form, not a SIGINT that kills the command — callers must treat
// ErrAborted as cancellation of the whole operation and never retry-loop
// on it, or Ctrl+C would trap the user in the prompt.
var ErrAborted = errors.New("prompt aborted")

// Prompter is the surface every command depends on for interactive
// input. Each method takes context.Context first so prompts inherit
// the caller's cancellation; the live impl wires ctx into the huh form
// (tea.WithContext), so cancellation tears the prompt down and the
// caller sees ctx.Err() immediately.
type Prompter interface {
	Confirm(ctx context.Context, msg string, def bool) (bool, error)
	Input(ctx context.Context, msg, def string) (string, error)
	Password(ctx context.Context, msg string) (string, error)
	Select(ctx context.Context, msg string, options []string) (int, error)
	MultiSelect(ctx context.Context, msg string, options []string) ([]int, error)
}
