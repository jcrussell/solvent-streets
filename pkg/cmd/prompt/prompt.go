// Package prompt defines the narrow Prompter interface that destructive or
// otherwise-interactive commands depend on. Per byob-prompter.1 the
// interface is intentionally small (five methods) so swapping the live
// implementation later — to charmbracelet/huh, AlecAivazis/survey, or
// anything else — touches one file rather than every caller. The live
// impl is stdlib-only (see live.go) because the repo's TUI stack already
// rides charm.land's bubbletea/v2; pulling in huh v1.x would fork the
// charm dep tree, which is too high a cost for what is currently a
// Confirm-only consumer (snapshot rm/prune via solvent-streets-y03).
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

// Prompter is the surface every command depends on for interactive
// input. Each method takes context.Context first so prompts inherit
// the caller's cancellation; the live impl honors ctx via a select on
// the read goroutine (stdin reads themselves are not ctx-aware, so a
// canceled prompt leaks the read goroutine until the user hits a key
// — the contract guarantees the caller sees ctx.Err() immediately).
type Prompter interface {
	Confirm(ctx context.Context, msg string, def bool) (bool, error)
	Input(ctx context.Context, msg, def string) (string, error)
	Password(ctx context.Context, msg string) (string, error)
	Select(ctx context.Context, msg string, options []string) (int, error)
	MultiSelect(ctx context.Context, msg string, options []string) ([]int, error)
}
