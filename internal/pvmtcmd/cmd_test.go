package pvmtcmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
)

// TestExitCode covers the mapping from error shape to process exit code.
// FlagError (and the unknown-command path that classifyUnknownCommand wraps
// as FlagError) → 2; ErrNoResults → 3; ErrCancel/context.Canceled → 0;
// everything else → 1. This is the cross-check on byob-errors.4: once the
// runner classifies "unknown command" as a flag error, a user typo
// (`pvmt nope`) must exit 2, not 1.
func TestExitCode(t *testing.T) {
	// exitCode is only invoked on non-nil errors (Main short-circuits to 0
	// on success), so the nil case is intentionally absent.
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"cancel sentinel", cmdutil.ErrCancel, 0},
		{"context canceled", context.Canceled, 0},
		{"flag error", &cmdutil.FlagError{Err: errors.New("--port out of range")}, 2},
		{"flag error wrapped", fmt.Errorf("wrap: %w", &cmdutil.FlagError{Err: errors.New("bad")}), 2},
		{"unknown command", errors.New(`unknown command "nope" for "pvmt"`), 2},
		{"no results", cmdutil.ErrNoResults, 3},
		{"silent sentinel", cmdutil.ErrSilent, 1},
		{"silent wrapped", fmt.Errorf("after streaming: %w", cmdutil.ErrSilent), 1},
		{"generic runtime error", errors.New("connection refused"), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			got := exitCode(tt.err, &buf)
			if got != tt.want {
				t.Fatalf("exitCode(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

// TestExitCode_SilentSuppressesPrint pins the contract that ErrSilent means
// "I already told the user" — the runner must not emit an extra "Error:"
// line. ErrNoResults shares this property; both are silent to keep the
// user's terminal free of redundant noise.
func TestExitCode_SilentSuppressesPrint(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
	}{
		{"silent sentinel", cmdutil.ErrSilent},
		{"silent wrapped", fmt.Errorf("downstream: %w", cmdutil.ErrSilent)},
		{"no results", cmdutil.ErrNoResults},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			_ = exitCode(tt.err, &buf)
			if buf.Len() != 0 {
				t.Fatalf("exitCode wrote %q, want empty", buf.String())
			}
		})
	}
}

// TestExitCode_UnknownCommandLooksLikeRealCommand guards against a false
// positive: a runtime error whose message *contains* "unknown command "
// in the middle must still exit 1. The classifier only triggers on the
// prefix, which is what cobra emits.
func TestExitCode_UnknownCommandLooksLikeRealCommand(t *testing.T) {
	err := errors.New("server replied: unknown command from upstream")
	var buf bytes.Buffer
	if got := exitCode(err, &buf); got != 1 {
		t.Fatalf("exit code = %d, want 1 (substring should not match prefix)", got)
	}
}

// TestPrintError_HintFormatting documents the multi-line indent behavior so
// a future refactor of printError can't silently regress it.
func TestPrintError_HintFormatting(t *testing.T) {
	err := cmdutil.Hintf(errors.New("boom"), "fix line one\nthen line two")
	var buf bytes.Buffer
	printError(&buf, err)
	out := buf.String()
	if !strings.Contains(out, "Error: boom\n") {
		t.Errorf("missing Error line: %q", out)
	}
	if !strings.Contains(out, "hint: fix line one\n") {
		t.Errorf("missing hint line: %q", out)
	}
	if !strings.Contains(out, "      then line two\n") {
		t.Errorf("missing indented continuation: %q", out)
	}
}
