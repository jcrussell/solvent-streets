package main

import (
	"bytes"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/config"
	"github.com/jcrussell/solvent-streets/pkg/cmd/root"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

// TestCobraTree_SubcommandRouting is the wiring smoke for the pvmt
// binary. cmd/pvmt has no other tests, so a typo in NewCmdRoot's
// addSubcommands (a dropped AddCommand, a misspelled Use string) would
// otherwise slip past `make test`. This pins the full set of expected
// command paths so the test fails in both directions: a missing
// AddCommand removes a path, and an unauthorized addition introduces an
// unexpected one. Update wantPaths intentionally when adding/removing a
// real subcommand.
func TestCobraTree_SubcommandRouting(t *testing.T) {
	rootCmd := newTestRoot(t)

	wantPaths := []string{
		"pvmt all",
		"pvmt all compute",
		"pvmt all ingest",
		"pvmt check-site",
		"pvmt cities",
		"pvmt config",
		"pvmt config show",
		"pvmt export",
		"pvmt forecast",
		"pvmt gc",
		"pvmt parking",
		"pvmt parking compute",
		"pvmt parking ingest",
		"pvmt parking status",
		"pvmt roads",
		"pvmt roads compute",
		"pvmt roads ingest",
		"pvmt roads status",
		"pvmt serve",
		"pvmt sidewalks",
		"pvmt sidewalks compute",
		"pvmt sidewalks ingest",
		"pvmt sidewalks status",
		"pvmt snapshots",
		"pvmt snapshots ls",
		"pvmt snapshots prune",
		"pvmt snapshots rm",
		"pvmt status",
		"pvmt version",
	}

	gotPaths := collectCommandPaths(rootCmd)

	if diff := diffSorted(wantPaths, gotPaths); diff != "" {
		t.Errorf("subcommand path set drifted (update wantPaths intentionally):\n%s", diff)
	}
}

// TestCobraTree_HelpForEverySubcommand asserts that `--help` succeeds
// and produces non-empty output for every command in the tree. Catches
// regressions that the path-set check misses: a command whose Short or
// flag wiring panics during help rendering, or a misnamed Use that
// cobra silently accepts but can't route through.
//
// Each iteration builds a fresh root because cobra's Command holds
// parsing state (flag values, captured args) across Execute calls.
func TestCobraTree_HelpForEverySubcommand(t *testing.T) {
	paths := collectCommandPaths(newTestRoot(t))
	for _, path := range paths {
		fields := strings.Fields(path)[1:] // drop "pvmt"
		args := append(append([]string(nil), fields...), "--help")
		t.Run(path, func(t *testing.T) {
			rootCmd := newTestRoot(t)
			var buf bytes.Buffer
			rootCmd.SetOut(&buf)
			rootCmd.SetErr(&buf)
			rootCmd.SetArgs(args)
			if err := rootCmd.Execute(); err != nil {
				t.Fatalf("execute %v: %v\n--- output ---\n%s", args, err, buf.String())
			}
			if !strings.Contains(buf.String(), "Usage:") {
				t.Errorf("%s --help: missing Usage: line\n--- output ---\n%s", path, buf.String())
			}
		})
	}
}

// newTestRoot returns a root command wired to a minimal Factory with
// captured iostreams. We deliberately avoid factory.New() here: it
// touches os.Stderr/os.Stdout and lazy-loads pvmt.toml from cwd, both
// of which would make this test flaky depending on where it runs.
func newTestRoot(t *testing.T) *cobra.Command {
	t.Helper()
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:      ios,
		LogLevel:       new(slog.LevelVar),
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		ExecutableName: "pvmt",
		Config: func() (*config.Config, error) {
			return &config.Config{Display: config.DisplayConfig{Units: "metric"}}, nil
		},
	}
	return root.NewCmdRoot(f)
}

// collectCommandPaths walks the tree and returns sorted "pvmt foo bar"
// paths for every visible subcommand. Cobra's auto-generated `help`
// and `completion` parents are skipped because their leaves vary by
// cobra version and aren't part of pvmt's wiring contract; the
// language-specific completion leaves (bash/zsh/fish/powershell) are
// also cobra-owned and hidden in our build via the completion
// subcommand's defaults.
func collectCommandPaths(rootCmd *cobra.Command) []string {
	var paths []string
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.Parent() != nil && !cmd.Hidden && cmd.Name() != "help" {
			paths = append(paths, cmd.CommandPath())
		}
		for _, sub := range cmd.Commands() {
			// Skip cobra's auto-generated `completion` subtree entirely —
			// its leaves (bash/zsh/fish/powershell) are cobra-owned and
			// not part of pvmt's wiring contract.
			if sub.Name() == "completion" && sub.Parent() == rootCmd {
				continue
			}
			walk(sub)
		}
	}
	walk(rootCmd)
	sort.Strings(paths)
	return paths
}

func diffSorted(want, got []string) string {
	wantSet := make(map[string]bool, len(want))
	for _, p := range want {
		wantSet[p] = true
	}
	gotSet := make(map[string]bool, len(got))
	for _, p := range got {
		gotSet[p] = true
	}
	var missing, extra []string
	for _, p := range want {
		if !gotSet[p] {
			missing = append(missing, p)
		}
	}
	for _, p := range got {
		if !wantSet[p] {
			extra = append(extra, p)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range missing {
		b.WriteString("  -- missing: ")
		b.WriteString(p)
		b.WriteString("\n")
	}
	for _, p := range extra {
		b.WriteString("  ++ extra:   ")
		b.WriteString(p)
		b.WriteString("\n")
	}
	return b.String()
}
