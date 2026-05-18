package pvmtcmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/pkg/cmdutil"

	"github.com/spf13/cobra"
)

// TestExitCode covers the mapping from error shape to process exit code.
// FlagError (and the usage-error paths that classifyUsageError wraps
// as FlagError) → 2; ErrNoResults → 3; ErrCancel/context.Canceled → 0;
// everything else → 1. Cross-check on byob-errors.4 and
// byob-command-shape.6: a `pvmt nope` typo and a cobra flag-group
// violation must both exit 2, not 1, since both are user errors.
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
		{"flag group mutually exclusive",
			errors.New("if any flags in the group [a b] are set none of the others can be; [a b] were all set"), 2},
		{"flag group required together",
			errors.New("if any flags in the group [a b] are set they must all be set; missing [b]"), 2},
		{"flag group one required",
			errors.New("at least one of the flags in the group [a b] is required"), 2},
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

// TestExitCode_FlagGroupPrefixIsAnchored guards classifyUsageError
// against substring matches on the flag-group prefixes. A runtime error
// quoting cobra's wording mid-message must still exit 1 — only a
// leading match (i.e., the error cobra actually emits) is classified
// as a usage error.
func TestExitCode_FlagGroupPrefixIsAnchored(t *testing.T) {
	for _, msg := range []string{
		"upstream said: if any flags in the group are weird, fail",
		"docs warn: at least one of the flags in the group should match",
	} {
		var buf bytes.Buffer
		if got := exitCode(errors.New(msg), &buf); got != 1 {
			t.Errorf("exit code for %q = %d, want 1", msg, got)
		}
	}
}

// TestClassifyUsageError_WrapsLiveCobraFlagGroupErrors locks in the
// integration end of byob-command-shape.6: when a real cobra command
// raises a flag-group violation via Execute(), the classifier matches
// the error string cobra emits today and wraps it as *FlagError. If a
// cobra upgrade ever rewords these messages, this test fails next to
// the prefix list in classifyUsageError, pointing the fix at the right
// half of the contract.
func TestClassifyUsageError_WrapsLiveCobraFlagGroupErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*cobra.Command)
		args  []string
	}{
		{
			name: "mutually exclusive",
			setup: func(c *cobra.Command) {
				c.Flags().Bool("a", false, "")
				c.Flags().Bool("b", false, "")
				c.MarkFlagsMutuallyExclusive("a", "b")
			},
			args: []string{"--a", "--b"},
		},
		{
			name: "required together",
			setup: func(c *cobra.Command) {
				c.Flags().String("key", "", "")
				c.Flags().String("secret", "", "")
				c.MarkFlagsRequiredTogether("key", "secret")
			},
			args: []string{"--key=k"},
		},
		{
			name: "one required",
			setup: func(c *cobra.Command) {
				c.Flags().String("file", "", "")
				c.Flags().String("url", "", "")
				c.MarkFlagsOneRequired("file", "url")
			},
			args: []string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &cobra.Command{
				Use:           "x",
				SilenceErrors: true,
				SilenceUsage:  true,
				RunE:          func(*cobra.Command, []string) error { return nil },
			}
			tc.setup(cmd)
			cmd.SetArgs(tc.args)
			err := cmd.Execute()
			if err == nil {
				t.Fatal("expected cobra to raise a flag-group violation")
			}
			classified := classifyUsageError(err)
			var flagErr *cmdutil.FlagError
			if !errors.As(classified, &flagErr) {
				t.Fatalf("classifyUsageError(%v) is not *FlagError: %T", err, classified)
			}
		})
	}
}

// TestPrintError_EmptyHintSuppressed pins the contract that a caller
// who constructs an *ErrHint with a zero Hint string (or Hintf-ed with
// an empty format) gets only the Error line — no dangling "hint:" with
// nothing after it. The runner branch `hint.Hint != ""` enforces this.
func TestPrintError_EmptyHintSuppressed(t *testing.T) {
	err := &cmdutil.ErrHint{Err: errors.New("boom"), Hint: ""}
	var buf bytes.Buffer
	printError(&buf, err)
	out := buf.String()
	if got, want := out, "Error: boom\n"; got != want {
		t.Fatalf("printError with empty hint: got %q, want %q", got, want)
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
