package cache

import (
	"context"
	"fmt"
	"time"

	httpcache "github.com/jcrussell/solvent-streets/internal/cache"
	"github.com/jcrussell/solvent-streets/pkg/cmd/prompt"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/spf13/cobra"
)

// defaultMaxSizeFlag is the --max-size default in the same notation a user
// would type. It must parse to httpcache.DefaultMaxSize; a test pins that.
const defaultMaxSizeFlag = "500mb"

type PruneOptions struct {
	IO       *iostreams.IOStreams
	Prompter prompt.Prompter
	// CacheDir resolves the HTTP cache directory. It is a func so the
	// per-OS path lookup stays lazy (and so tests can point at a temp
	// dir without touching the real cache).
	CacheDir func() (string, error)

	MaxAge  time.Duration
	MaxSize string
	// MaxSizeBytes is MaxSize parsed; RunE fills it in so a bad value is
	// a flag error (exit 2) instead of a mid-run failure.
	MaxSizeBytes int64
	DryRun       bool
	Yes          bool
}

func NewCmdPrune(f *cmdutil.Factory, runF func(context.Context, *PruneOptions) error) *cobra.Command {
	opts := &PruneOptions{
		IO:       f.IOStreams,
		Prompter: f.Prompter,
		CacheDir: func() (string, error) {
			p, err := f.Paths()
			if err != nil {
				return "", err
			}
			return p.HTTPCacheDir(), nil
		},
	}

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Reclaim disk from the HTTP response cache",
		Long: `Bound the on-disk HTTP cache, which otherwise grows forever: nothing
in the request path ever evicts an entry.

Three sweeps run, in order:

  - Incomplete entries. Each cache entry is a body/metadata file pair;
    a crash between the two writes, or a leftover temp file, leaves
    bytes that can never be served. These are always removed.
  - Age. Entries untouched for longer than --max-age are removed.
    Entries are revalidated every 24h, so a month-old entry has gone
    ~30 windows without being wanted.
  - Size. If the cache is still over --max-size, whole entries are
    removed oldest-first until it fits. The ceiling covers the cache
    entries prune manages; bytes it deliberately leaves alone (a
    subdirectory, a file pvmt did not write, an entry currently being
    written) are not counted against it.

An entry's age is the more recent of its two files, so an entry that a
304 refreshed an hour ago is treated as an hour old even when its body
was downloaded months ago.

No stored data is lost: pruning only discards downloaded responses. It
is not free, though — every removed entry is re-fetched from Overpass /
ArcGIS / Nominatim on next use, so the cost is bandwidth and load on
community-funded endpoints. Unlike gc, this command reads no config and
opens no database, so it works when either is broken.

Pass 0 to disable a cap (--max-age=0 skips age pruning, --max-size=0
skips size pruning); incomplete entries are still swept.

Confirmation: prompts on TTY by default. Pass --yes/-y to skip the
prompt for non-interactive use (scripts, CI). Without --yes and without
a TTY the command refuses to delete. --dry-run reports what would go,
never prompts, and writes nothing.`,
		Example: `  # Report what would be reclaimed, without deleting or prompting
  pvmt cache prune --dry-run

  # Apply the default caps (30 days, 500 MiB); prompts before deleting
  pvmt cache prune

  # Skip the confirmation prompt
  pvmt cache prune --yes

  # Keep only the last week, capped at 100 MiB
  pvmt cache prune --max-age=168h --max-size=100mb --yes

  # Size cap only
  pvmt cache prune --max-age=0 --max-size=1g --yes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.MaxAge < 0 {
				return cmdutil.FlagErrorf("--max-age must not be negative, got %s", opts.MaxAge)
			}
			n, err := httpcache.ParseSize(opts.MaxSize)
			if err != nil {
				return cmdutil.FlagErrorf("--max-size: %w", err)
			}
			opts.MaxSizeBytes = n
			if runF != nil {
				return runF(cmd.Context(), opts)
			}
			return runPrune(cmd.Context(), opts)
		},
	}

	cmd.Flags().DurationVar(&opts.MaxAge, "max-age", httpcache.DefaultMaxAge,
		"Remove entries not written or revalidated within this duration (0 disables)")
	cmd.Flags().StringVar(&opts.MaxSize, "max-size", defaultMaxSizeFlag,
		"Ceiling on the total size of managed cache entries, e.g. 500mb or 2g; binary units (0 disables)")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Report what would be removed without removing or prompting")
	cmd.Flags().BoolVarP(&opts.Yes, "yes", "y", false, "Skip the interactive confirmation")

	return cmd
}

// runPrune plans the sweep, confirms it, then applies it.
//
// The plan pass is a real Prune with DryRun forced on, mirroring gc's
// scan-then-sweep shape: it lets the prompt quote a true total, and a
// declined prompt leaves the cache untouched because the plan pass wrote
// nothing. Re-scanning for the apply pass costs one readdir and is
// race-tolerant by design — the numbers the user approved are an estimate
// of a directory another process may be writing to either way.
func runPrune(ctx context.Context, opts *PruneOptions) error {
	dir, err := opts.CacheDir()
	if err != nil {
		return err
	}
	sweep := httpcache.PruneOptions{MaxAge: opts.MaxAge, MaxSize: opts.MaxSizeBytes}

	plan, planErr := httpcache.Prune(dir, withDryRun(sweep, true))
	printPruneReport(opts, dir, plan)
	if planErr != nil {
		return planErr
	}

	victims := plan.EntriesRemoved + plan.OrphansRemoved
	if victims == 0 {
		return nil
	}
	if opts.DryRun {
		fmt.Fprintf(opts.IO.ErrOut, "\nDry run: %s would be reclaimed; nothing deleted.\n",
			cmdutil.HumanSize(plan.BytesReclaimed))
		return nil
	}

	// Removing an entry is not destructive to stored data, but it is not
	// free either: every evicted entry is re-downloaded on next use, and
	// the upstreams are community-funded (Overpass, Nominatim). Say so
	// before asking.
	fmt.Fprintf(opts.IO.ErrOut,
		"\nRemoved entries are re-fetched from Overpass/ArcGIS/Nominatim on next use.\n")
	if err := cmdutil.ConfirmDestructive(ctx, opts.IO, opts.Prompter, opts.Yes,
		fmt.Sprintf("Remove %d cache entr%s and reclaim %s?", victims, plural(victims),
			cmdutil.HumanSize(plan.BytesReclaimed)),
		fmt.Sprintf("refusing to remove %d cache entr%s without confirmation", victims, plural(victims)),
	); err != nil {
		return err
	}

	report, pruneErr := httpcache.Prune(dir, withDryRun(sweep, false))
	// Print the outcome even on a partial failure — the caller still
	// wants to know what was reclaimed before the error. This is a
	// summary rather than a repeat of the block above; the counts are
	// what actually happened, which can differ from the plan if another
	// process touched the cache in between.
	fmt.Fprintf(opts.IO.ErrOut, "\nRemoved %d entr%s: %d by age, %d over size cap, %d incomplete.\n",
		report.EntriesRemoved+report.OrphansRemoved, plural(report.EntriesRemoved+report.OrphansRemoved),
		report.AgeRemoved, report.SizeRemoved, report.OrphansRemoved)
	fmt.Fprintf(opts.IO.ErrOut, "Reclaimed %s; %s remaining.\n",
		cmdutil.HumanSize(report.BytesReclaimed), cmdutil.HumanSize(report.BytesRemaining))
	return pruneErr
}

func withDryRun(o httpcache.PruneOptions, dryRun bool) httpcache.PruneOptions {
	o.DryRun = dryRun
	return o
}

// printPruneReport writes the plan block shown before the confirmation
// prompt. Everything it describes is hypothetical — the pass that
// produced r wrote nothing.
func printPruneReport(opts *PruneOptions, dir string, r *httpcache.PruneReport) {
	w := opts.IO.ErrOut
	row := func(label, format string, args ...any) {
		fmt.Fprintf(w, "  %-13s %s\n", label+":", fmt.Sprintf(format, args...))
	}

	fmt.Fprintf(w, "Cache: %s\n", dir)
	// "in entries" is deliberate: this total covers the `.json`/`.meta`
	// pairs prune manages, and excludes any skipped path (a subdirectory
	// or a file pvmt did not write), whose bytes prune never measures.
	row("scanned", "%d entr%s, %s in entries", r.EntriesScanned, plural(r.EntriesScanned),
		cmdutil.HumanSize(r.BytesReclaimed+r.BytesRemaining))
	// Printed before the early return: a stray subdirectory or symlink is
	// most worth flagging in exactly the run where nothing else happened.
	if r.Skipped > 0 {
		row("skipped", "%d path(s) left alone (subdirs, symlinks, or files pvmt did not write)", r.Skipped)
	}

	if r.EntriesRemoved+r.OrphansRemoved == 0 {
		fmt.Fprintln(w, "Nothing to prune.")
		return
	}

	row("would remove", "%d by age, %d over size cap, %d incomplete",
		r.AgeRemoved, r.SizeRemoved, r.OrphansRemoved)
	row("reclaimed", "%s", cmdutil.HumanSize(r.BytesReclaimed))
	row("remaining", "%s", cmdutil.HumanSize(r.BytesRemaining))
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
