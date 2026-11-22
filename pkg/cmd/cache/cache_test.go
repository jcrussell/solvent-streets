package cache

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
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

// writeEntry lays down a complete cache pair (<key>.json.gz + <key>.meta)
// with the given mtime, using a
// well-formed 64-hex-char key so prune recognizes it.
func writeEntry(t *testing.T, dir string, n int, age time.Duration) (string, string) {
	t.Helper()
	const size = 100
	k := strings.Repeat("0", 63) + string("0123456789abcdef"[n%16])
	body := filepath.Join(dir, k+".json.gz")
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
			if _, ok := errors.AsType[*cmdutil.FlagError](err); !ok {
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

	ios, _, stdout, _ := iostreams.Test()
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

	// The report is the plan, which names the directory, then the outcome.
	// (TestRunPrune_DryRunLeavesDiskAlone tells that outcome from a dry
	// run's.)
	blocks := reportBlocks(stdout.String())
	if len(blocks) != 2 || !strings.Contains(blocks[0], dir) {
		t.Fatalf("report should be a plan naming %s and an outcome, got %d blocks:\n%s", dir, len(blocks), stdout.String())
	}
	// The outcome gives the size reclaimed (the stale pair) and the size
	// remaining (the fresh pair): 200 bytes each.
	if size := cmdutil.HumanSize(200); strings.Count(blocks[1], size) != 2 {
		t.Errorf("outcome %q should give %s reclaimed and %s remaining", blocks[1], size, size)
	}
}

// endsAcknowledged reports whether a plan block closes with a line of its
// own after the indented rows: the acknowledgement that there is nothing to
// prune.
func endsAcknowledged(plan string) bool {
	lines := strings.Split(plan, "\n")
	return len(lines) > 1 && !strings.HasPrefix(lines[len(lines)-1], " ")
}

// reportBlocks splits prune's text report into its blank-line-separated
// blocks: the plan, then, when there was something to remove, the outcome.
func reportBlocks(out string) []string {
	var blocks []string
	for b := range strings.SplitSeq(out, "\n\n") {
		if strings.TrimSpace(b) != "" {
			blocks = append(blocks, strings.TrimSpace(b))
		}
	}
	return blocks
}

// TestRunPrune_DryRunLeavesDiskAlone: the report describes the sweep, the
// files are all still there.
func TestRunPrune_DryRunLeavesDiskAlone(t *testing.T) {
	dir := t.TempDir()
	body, meta := writeEntry(t, dir, 1, 90*24*time.Hour)

	ios, _, stdout, _ := iostreams.Test()
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
	dry := reportBlocks(stdout.String())
	if len(dry) != 2 {
		t.Fatalf("dry-run report should be a plan and a summary, got %d blocks:\n%s", len(dry), stdout.String())
	}

	// The same sweep for real plans the same removal, removes the files, and
	// says something else about the outcome: the dry run's summary does not
	// read as a deletion.
	stdout.Reset()
	opts.DryRun, opts.Yes = false, true
	if err := runPrune(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{body, meta} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("the real run left %s", filepath.Base(p))
		}
	}
	applied := reportBlocks(stdout.String())
	if len(applied) != 2 || applied[0] != dry[0] || applied[1] == dry[1] {
		t.Errorf("real report =\n%s\nwant the dry run's plan and a different outcome; dry run was:\n%s",
			strings.Join(applied, "\n\n"), strings.Join(dry, "\n\n"))
	}
}

// TestRunPrune_EmptyCacheSaysSo keeps the no-op path from printing a
// removal summary of all zeros.
func TestRunPrune_EmptyCacheSaysSo(t *testing.T) {
	ios, _, stdout, _ := iostreams.Test()
	opts := &PruneOptions{
		IO:       ios,
		CacheDir: func() (string, error) { return filepath.Join(t.TempDir(), "absent"), nil },
		MaxAge:   httpcache.DefaultMaxAge,
	}
	if err := runPrune(context.Background(), opts); err != nil {
		t.Fatalf("pruning a cache that was never created must not error: %v", err)
	}
	// The plan alone: no outcome block follows it.
	if blocks := reportBlocks(stdout.String()); len(blocks) != 1 || !endsAcknowledged(blocks[0]) {
		t.Errorf("want the plan, closed by a line saying there is nothing to prune, and no removal summary; got %d blocks:\n%s", len(blocks), stdout.String())
	}
}

// TestNewCmdPrune_ResolvesHTTPCacheDirFromPaths: the
// command must reach the same directory the client factory writes, via
// Paths.HTTPCacheDir, with no duplicated "http" literal.
func TestNewCmdPrune_ResolvesHTTPCacheDirFromPaths(t *testing.T) {
	root := t.TempDir()
	httpDir := filepath.Join(root, "http")
	if err := os.MkdirAll(httpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body, meta := writeEntry(t, httpDir, 1, 90*24*time.Hour)

	ios, _, stdout, _ := iostreams.Test()
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

	if !strings.Contains(stdout.String(), httpDir) {
		t.Errorf("report should name %s, got:\n%s", httpDir, stdout.String())
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
	// The re-fetch cost is stated on stderr before the question (the stub
	// prompter prints nothing itself).
	if strings.TrimSpace(stderr.String()) == "" {
		t.Error("expected the re-fetch cost to be disclosed on stderr before the prompt")
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
	if _, ok := errors.AsType[*cmdutil.FlagError](err); !ok {
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

	ios, _, stdout, _ := iostreams.Test()
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
	// The plan and its dry-run summary were printed without asking.
	if blocks := reportBlocks(stdout.String()); len(blocks) != 2 {
		t.Errorf("expected a plan and a dry-run summary, got %d blocks:\n%s", len(blocks), stdout.String())
	}
}

// TestRunPrune_NothingToPruneNeverPrompts: with no victims there is
// nothing to authorize, so an empty cache must not stop to ask.
func TestRunPrune_NothingToPruneNeverPrompts(t *testing.T) {
	dir := t.TempDir()
	fresh, _ := writeEntry(t, dir, 1, time.Hour) // nothing to do

	ios, _, stdout, _ := iostreams.Test()
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
	// The plan alone, and the fresh entry is still there.
	if blocks := reportBlocks(stdout.String()); len(blocks) != 1 || !endsAcknowledged(blocks[0]) {
		t.Errorf("want the plan, closed by a line saying there is nothing to prune, and no outcome; got %d blocks:\n%s", len(blocks), stdout.String())
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("the fresh entry was removed: %v", err)
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

	// planOf is the idle run's report for cacheDir: one block, the plan.
	planOf := func(cacheDir string) []string {
		t.Helper()
		ios, _, stdout, _ := iostreams.Test()
		err := runPrune(context.Background(), &PruneOptions{
			IO:       ios,
			CacheDir: func() (string, error) { return cacheDir, nil },
			MaxAge:   httpcache.DefaultMaxAge,
		})
		if err != nil {
			t.Fatal(err)
		}
		blocks := reportBlocks(stdout.String())
		if len(blocks) != 1 || !endsAcknowledged(blocks[0]) {
			t.Fatalf("want the plan, closed by a line saying there is nothing to prune, and no outcome; got %d blocks:\n%s", len(blocks), stdout.String())
		}
		return strings.Split(blocks[0], "\n")
	}
	// The stray path adds a row to the plan an empty cache prints, and the
	// row gives the count.
	empty, withStray := planOf(t.TempDir()), planOf(dir)
	if len(withStray) != len(empty)+1 {
		t.Fatalf("plan with a skipped path has %d lines, want one more than the empty cache's %d:\n%s",
			len(withStray), len(empty), strings.Join(withStray, "\n"))
	}
	// Line 0 names the directory, which differs between the two runs.
	extra := slices.IndexFunc(withStray[1:], func(l string) bool { return !slices.Contains(empty, l) })
	if extra < 0 || !strings.Contains(withStray[extra+1], "1") {
		t.Errorf("plan =\n%s\nwant one row the empty cache's lacks, counting the 1 path left alone", strings.Join(withStray, "\n"))
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
	// The group itself only aggregates: its RunE shows help, and a stray
	// argument (a typo'd subcommand) is refused rather than shown help and
	// exit 0 (pinned end-to-end in internal/pvmtcmd).
	if cmd.Args == nil || cmd.Args(cmd, []string{"pruen"}) == nil {
		t.Error("the cache group should reject stray arguments")
	}
}

// TestRunPrune_JSON: --json prints one document, the plan's on a dry run and
// the sweep's otherwise, and no text report.
func TestRunPrune_JSON(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		dir := t.TempDir()
		writeEntry(t, dir, 1, 90*24*time.Hour)
		writeEntry(t, dir, 2, time.Hour)
		ios, _, stdout, _ := iostreams.Test()
		args := []string{"--json", "--yes"}
		if dryRun {
			args = append(args, "--dry-run")
		}
		var got *PruneOptions
		cmd := NewCmdPrune(&cmdutil.Factory{IOStreams: ios}, func(_ context.Context, o *PruneOptions) error {
			got = o
			return nil
		})
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		got.CacheDir = func() (string, error) { return dir, nil }
		if err := runPrune(context.Background(), got); err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
			t.Fatalf("dry run %v: %v: %s", dryRun, err, stdout.String())
		}
		if doc["scanned_count"] != 2.0 || doc["age_removed_count"] != 1.0 || doc["deleted"] != !dryRun || doc["dir"] != dir {
			t.Errorf("dry run %v: doc = %v", dryRun, doc)
		}
	}
}
