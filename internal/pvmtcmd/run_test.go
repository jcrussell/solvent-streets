package pvmtcmd

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/pkg/cmd/root"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

func testFactory() (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer) {
	ios, _, out, errOut := iostreams.Test()
	return &cmdutil.Factory{
		IOStreams: ios,
		LogLevel:  new(slog.LevelVar),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: func() (*config.Config, error) {
			return &config.Config{Display: config.DisplayConfig{Units: "metric"}}, nil
		},
	}, out, errOut
}

// parentPaths lists the command path (minus the root) of every command that
// has subcommands, so a new group gets the typo check without editing this
// test.
func parentPaths(t *testing.T) [][]string {
	t.Helper()
	f, _, _ := testFactory()
	var paths [][]string
	var walk func(c *cobra.Command, path []string)
	walk = func(c *cobra.Command, path []string) {
		for _, sub := range c.Commands() {
			if !sub.HasSubCommands() || sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			p := append(append([]string(nil), path...), sub.Name())
			paths = append(paths, p)
			walk(sub, p)
		}
	}
	walk(root.NewCmdRoot(f), nil)
	if len(paths) == 0 {
		t.Fatal("found no parent commands; the tree walk is broken")
	}
	return paths
}

// TestRun_TyposExitTwo pins byob-command-shape.6: every typo is a usage
// error — exit 2, nothing on stdout, the typo named on stderr. Cobra alone
// turns `help <typo>` and `<group> <typo>` into help text and exit 0, which
// a `pvmt cache pruen && …` chain reads as success.
func TestRun_TyposExitTwo(t *testing.T) {
	cases := [][]string{
		{"biuld"},
		{"help", "biuld"},
		{"help", "cache", "biuld"},
	}
	for _, p := range parentPaths(t) {
		cases = append(cases, append(append([]string(nil), p...), "biuld"))
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f, out, errOut := testFactory()
			if got := run(context.Background(), f, args); got != 2 {
				t.Fatalf("got exit code %d, want 2\nstdout: %s\nstderr: %s", got, out, errOut)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want empty", out.String())
			}
			if !strings.Contains(errOut.String(), "biuld") {
				t.Errorf("stderr = %q, want it to name the typo", errOut.String())
			}
		})
	}
}

// TestRun_HelpStillWorks is the other half: the typo guards must not turn a
// legitimate help request into an error.
func TestRun_HelpStillWorks(t *testing.T) {
	cases := [][]string{
		{"help"},
		{"help", "cache"},
		{"help", "cache", "prune"},
		{"--help"},
	}
	for _, p := range parentPaths(t) {
		cases = append(cases, p, append(append([]string(nil), p...), "--help"))
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f, out, errOut := testFactory()
			if got := run(context.Background(), f, args); got != 0 {
				t.Fatalf("got exit code %d, want 0\nstderr: %s", got, errOut)
			}
			if !strings.Contains(out.String(), "Usage:") {
				t.Errorf("stdout = %q, want help text", out.String())
			}
		})
	}
}
