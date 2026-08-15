package cache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	httpcache "github.com/jcrussell/solvent-streets/internal/cache"
	"github.com/jcrussell/solvent-streets/internal/paths"
	"github.com/jcrussell/solvent-streets/pkg/cmd/prompt"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// writeEntry lays down a complete cache pair with the given mtime, using a
// well-formed 64-hex-char key so prune recognizes it.
func writeEntry(t *testing.T, dir string, n int, age time.Duration) (string, string) {
	t.Helper()
	const size = 100
	k := strings.Repeat("0", 63) + string("0123456789abcdef"[n%16])
	body := filepath.Join(dir, k+".json")
	meta := filepath.Join(dir, k+".meta")
	for _, p := range []string{body, meta} {
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
	return body, meta
}

// TestNewCmdPrune_RunFInjection pins the test-injection seam.
func TestNewCmdPrune_RunFInjection(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}
	var got *PruneOptions
	cmd := NewCmdPrune(f, func(_ context.Context, o *PruneOptions) error {
		got = o
		return nil
	})
	cmd.SetArgs([]string{"--max-age=1h", "--max-size=2k", "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("runF was not called")
	}
	if got.MaxAge != time.Hour {
		t.Errorf("MaxAge = %s, want 1h", got.MaxAge)
	}
	if got.MaxSizeBytes != 2048 {
		t.Errorf("MaxSizeBytes = %d, want 2048 (--max-size must be parsed before runF)", got.MaxSizeBytes)
	}
	if !got.DryRun {
		t.Error("DryRun was not carried onto Options")
	}
}

// TestNewCmdPrune_Defaults ties the flag defaults to the package
// constants, so changing one without the other fails here rather than
// silently shipping a different cap than the help text advertises.
func TestNewCmdPrune_Defaults(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	cmd := NewCmdPrune(&cmdutil.Factory{IOStreams: ios}, func(context.Context, *PruneOptions) error { return nil })

	if got := cmd.Flags().Lookup("max-age").DefValue; got != httpcache.DefaultMaxAge.String() {
		t.Errorf("--max-age default = %q, want %q", got, httpcache.DefaultMaxAge)
	}
	if got := cmd.Flags().Lookup("max-size").DefValue; got != defaultMaxSizeFlag {
		t.Errorf("--max-size default = %q, want %q", got, defaultMaxSizeFlag)
	}
	n, err := httpcache.ParseSize(defaultMaxSizeFlag)
	if err != nil {
		t.Fatalf("the --max-size default must itself be parseable: %v", err)
	}
	if n != httpcache.DefaultMaxSize {
		t.Errorf("default flag %q = %d bytes, want DefaultMaxSize %d", defaultMaxSizeFlag, n, httpcache.DefaultMaxSize)
	}
}

// TestNewCmdPrune_BadFlagsAreFlagErrors: bad user input must surface as a
// typed FlagError (exit 2), not a generic failure.
func TestNewCmdPrune_BadFlagsAreFlagErrors(t *testing.T) {
	for _, arg := range []string{"--max-size=banana", "--max-size=10x", "--max-age=-5m"} {
		t.Run(arg, func(t *testing.T) {
			ios, _, _, _ := iostreams.Test()
			cmd := NewCmdPrune(&cmdutil.Factory{IOStreams: ios}, func(context.Context, *PruneOptions) error {
				t.Error("runF ran despite an invalid flag")
				return nil
			})
			cmd.SetArgs([]string{arg})
			cmd.SetOut(ios.Out)
			cmd.SetErr(ios.ErrOut)
			err := cmd.Execute()
			if err == nil {
				t.Fatalf("%s: expected an error", arg)
			}
			var fe *cmdutil.FlagError
			if !errors.As(err, &fe) {
				t.Errorf("%s: error is %T, want *cmdutil.FlagError", arg, err)
			}
		})
	}
}

// TestRunPrune_RemovesStaleEntriesAndReports is the end-to-end command
// path: a stale entry is gone from disk and the report names it.
func TestRunPrune_RemovesStaleEntriesAndReports(t *testing.T) {
	dir := t.TempDir()
	staleBody, staleMeta := writeEntry(t, dir, 1, 90*24*time.Hour)
	freshBody, _ := writeEntry(t, dir, 2, time.Hour)

	ios, _, _, stderr := iostreams.Test()
	opts := &PruneOptions{
		IO:           ios,
		CacheDir:     func() (string, error) { return dir, nil },
		MaxAge:       httpcache.DefaultMaxAge,
		MaxSizeBytes: httpcache.DefaultMaxSize,
		Yes:          true,
	}
	if err := runPrune(context.Background(), opts); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(staleBody); err == nil {
		t.Error("stale body survived")
	}
	if _, err := os.Stat(staleMeta); err == nil {
		t.Error("stale meta survived")
	}
	if _, err := os.Stat(freshBody); err != nil {
		t.Error("fresh entry was removed")
	}

	out := stderr.String()
	for _, want := range []string{dir, "scanned", "Removed", "Reclaimed", "remaining"} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Dry run") {
		t.Errorf("non-dry-run reported as a dry run:\n%s", out)
	}
}

// TestRunPrune_DryRunLeavesDiskAlone: the report describes the sweep, the
// files are all still there.
func TestRunPrune_DryRunLeavesDiskAlone(t *testing.T) {
	dir := t.TempDir()
	body, meta := writeEntry(t, dir, 1, 90*24*time.Hour)

	ios, _, _, stderr := iostreams.Test()
	opts := &PruneOptions{
		IO:       ios,
		CacheDir: func() (string, error) { return dir, nil },
		MaxAge:   httpcache.DefaultMaxAge,
		DryRun:   true,
	}
	if err := runPrune(context.Background(), opts); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{body, meta} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("--dry-run deleted %s", filepath.Base(p))
		}
	}
	out := stderr.String()
	if !strings.Contains(out, "would remove") || !strings.Contains(out, "Dry run") {
		t.Errorf("dry-run report does not read as hypothetical:\n%s", out)
	}
}

// TestRunPrune_EmptyCacheSaysSo keeps the no-op path from printing a
// removal summary of all zeros.
func TestRunPrune_EmptyCacheSaysSo(t *testing.T) {
	ios, _, _, stderr := iostreams.Test()
	opts := &PruneOptions{
		IO:       ios,
		CacheDir: func() (string, error) { return filepath.Join(t.TempDir(), "absent"), nil },
		MaxAge:   httpcache.DefaultMaxAge,
	}
	if err := runPrune(context.Background(), opts); err != nil {
		t.Fatalf("pruning a cache that was never created must not error: %v", err)
	}
	if !strings.Contains(stderr.String(), "Nothing to prune.") {
		t.Errorf("want a 'Nothing to prune.' line, got:\n%s", stderr.String())
	}
}

// TestNewCmdPrune_ResolvesHTTPCacheDirFromPaths pins step 1 of b136: the
// command must reach the same directory the client factory writes, via
// Paths.HTTPCacheDir, with no duplicated "http" literal.
func TestNewCmdPrune_ResolvesHTTPCacheDirFromPaths(t *testing.T) {
	root := t.TempDir()
	httpDir := filepath.Join(root, "http")
	if err := os.MkdirAll(httpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body, meta := writeEntry(t, httpDir, 1, 90*24*time.Hour)

	ios, _, _, stderr := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams: ios,
		Paths:     func() (*paths.Paths, error) { return &paths.Paths{Cache: root}, nil },
	}
	cmd := NewCmdPrune(f, nil)
	cmd.SetArgs([]string{"--yes"})
	cmd.SetOut(ios.Out)
	cmd.SetErr(ios.ErrOut)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(stderr.String(), httpDir) {
		t.Errorf("report should name %s, got:\n%s", httpDir, stderr.String())
	}
	for _, p := range []string{body, meta} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s survived; the command did not sweep Paths.HTTPCacheDir", filepath.Base(p))
		}
	}
}

// TestRunPrune_ConcurrentInvocationsDoNotError: two pvmt processes may
// prune at once, and losing the race to unlink a file is not a failure.
func TestRunPrune_ConcurrentInvocationsDoNotError(t *testing.T) {
	dir := t.TempDir()
	for i := range 16 {
		writeEntry(t, dir, i, 90*24*time.Hour)
	}

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Go(func() {
			ios, _, _, _ := iostreams.Test()
			errs[i] = runPrune(context.Background(), &PruneOptions{
				IO:       ios,
				CacheDir: func() (string, error) { return dir, nil },
				MaxAge:   time.Hour,
				Yes:      true,
			})
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent prune %d errored: %v", i, err)
		}
	}
}

// TestRunPrune_PromptsOnTTY: an interactive run must ask before deleting
// and must proceed on "yes". The default `pvmt cache prune` can evict
// gigabytes that cost a full re-download from community-funded
// endpoints, so it gets the same gate `pvmt gc` has.
func TestRunPrune_PromptsOnTTY(t *testing.T) {
	dir := t.TempDir()
	body, meta := writeEntry(t, dir, 1, 90*24*time.Hour)

	ios, _, _, stderr := iostreams.Test()
	ios.SetStdinTTY(true)
	stub := &prompt.Stub{Confirms: []bool{true}}
	err := runPrune(context.Background(), &PruneOptions{
		IO:       ios,
		Prompter: stub,
		CacheDir: func() (string, error) { return dir, nil },
		MaxAge:   httpcache.DefaultMaxAge,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stub.Confirms) != 0 {
		t.Error("Prompter.Confirm was not consumed; the sweep did not prompt")
	}
	for _, p := range []string{body, meta} {
		if _, statErr := os.Stat(p); statErr == nil {
			t.Errorf("%s survived an approved prune", filepath.Base(p))
		}
	}
	// The re-fetch cost must be stated before the question, not buried.
	if !strings.Contains(stderr.String(), "re-fetched") {
		t.Errorf("expected the re-fetch cost to be disclosed, got:\n%s", stderr.String())
	}
}

// TestRunPrune_CancelOnPromptNo: declining must leave every byte in
// place and return ErrCancel (exit 0), not an error.
func TestRunPrune_CancelOnPromptNo(t *testing.T) {
	dir := t.TempDir()
	body, meta := writeEntry(t, dir, 1, 90*24*time.Hour)

	ios, _, _, _ := iostreams.Test()
	ios.SetStdinTTY(true)
	stub := &prompt.Stub{Confirms: []bool{false}}
	err := runPrune(context.Background(), &PruneOptions{
		IO:       ios,
		Prompter: stub,
		CacheDir: func() (string, error) { return dir, nil },
		MaxAge:   httpcache.DefaultMaxAge,
	})
	if !errors.Is(err, cmdutil.ErrCancel) {
		t.Fatalf("err = %v, want ErrCancel", err)
	}
	for _, p := range []string{body, meta} {
		if _, statErr := os.Stat(p); statErr != nil {
			t.Errorf("%s was deleted after the prompt was declined", filepath.Base(p))
		}
	}
}

// TestRunPrune_NoTTYWithoutYesRefuses: a piped/CI invocation must refuse
// rather than silently deleting, and say how to proceed.
func TestRunPrune_NoTTYWithoutYesRefuses(t *testing.T) {
	dir := t.TempDir()
	body, _ := writeEntry(t, dir, 1, 90*24*time.Hour)

	ios, _, _, _ := iostreams.Test() // stdin is not a TTY by default
	err := runPrune(context.Background(), &PruneOptions{
		IO:       ios,
		CacheDir: func() (string, error) { return dir, nil },
		MaxAge:   httpcache.DefaultMaxAge,
	})
	if err == nil {
		t.Fatal("expected a refusal without --yes and without a TTY")
	}
	var fe *cmdutil.FlagError
	if !errors.As(err, &fe) {
		t.Errorf("error is %T, want *cmdutil.FlagError (exit 2)", err)
	}
	if _, statErr := os.Stat(body); statErr != nil {
		t.Error("files were deleted despite the refusal")
	}
}

// TestRunPrune_DryRunNeverPrompts is the interaction the reviewer called
// out: --dry-run deletes nothing, so asking permission would be
// nonsense — and on a TTY a prompt would hang a scripted `--dry-run`.
// The nil Prompter here would panic if the code tried to confirm.
func TestRunPrune_DryRunNeverPrompts(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, 1, 90*24*time.Hour)

	ios, _, _, stderr := iostreams.Test()
	ios.SetStdinTTY(true)
	err := runPrune(context.Background(), &PruneOptions{
		IO:       ios,
		Prompter: nil, // any confirm attempt panics
		CacheDir: func() (string, error) { return dir, nil },
		MaxAge:   httpcache.DefaultMaxAge,
		DryRun:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "Dry run") {
		t.Errorf("expected a dry-run summary, got:\n%s", stderr.String())
	}
}

// TestRunPrune_NothingToPruneNeverPrompts: with no victims there is
// nothing to authorize, so an empty cache must not stop to ask.
func TestRunPrune_NothingToPruneNeverPrompts(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, 1, time.Hour) // fresh; nothing to do

	ios, _, _, stderr := iostreams.Test()
	ios.SetStdinTTY(true)
	err := runPrune(context.Background(), &PruneOptions{
		IO:       ios,
		Prompter: nil, // any confirm attempt panics
		CacheDir: func() (string, error) { return dir, nil },
		MaxAge:   httpcache.DefaultMaxAge,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "Nothing to prune.") {
		t.Errorf("want 'Nothing to prune.', got:\n%s", stderr.String())
	}
}

// TestRunPrune_SkippedPathsReportedWhenIdle: a stray subdirectory or
// symlink is most worth surfacing in the run where nothing else
// happened, which is exactly where an early return could hide it.
func TestRunPrune_SkippedPathsReportedWhenIdle(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}

	ios, _, _, stderr := iostreams.Test()
	err := runPrune(context.Background(), &PruneOptions{
		IO:       ios,
		CacheDir: func() (string, error) { return dir, nil },
		MaxAge:   httpcache.DefaultMaxAge,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := stderr.String()
	if !strings.Contains(out, "skipped") {
		t.Errorf("skipped paths must be reported even when nothing is pruned, got:\n%s", out)
	}
	if !strings.Contains(out, "Nothing to prune.") {
		t.Errorf("want 'Nothing to prune.', got:\n%s", out)
	}
}

// TestNewCmdCache_WiresPrune guards the group's only job.
func TestNewCmdCache_WiresPrune(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	cmd := NewCmdCache(&cmdutil.Factory{IOStreams: ios})
	if cmd.Name() != "cache" {
		t.Errorf("Name = %q, want %q", cmd.Name(), "cache")
	}
	sub, _, err := cmd.Find([]string{"prune"})
	if err != nil || sub.Name() != "prune" {
		t.Fatalf("`cache prune` not registered: %v", err)
	}
	// The group itself must not run anything — it only aggregates.
	if cmd.RunE != nil || cmd.Run != nil {
		t.Error("the cache group should have no runFunc of its own")
	}
}
