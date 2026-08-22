package cache

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
)

// The disk cache written by CachingTransport is append-only: every miss
// adds a `<key>.json` / `<key>.meta` pair and nothing ever removes one
// (readCache's corrupt-meta cleanup is deliberately narrow — it only
// fires on an entry it just failed to parse). A long-lived install
// therefore grows without bound, and keys that will never be requested
// again (a bbox that changed, a city removed from pvmt.toml) are never
// reclaimed. Prune is the eviction half: an explicit, offline sweep
// driven by `pvmt cache prune`, not an online policy on the hot path —
// evicting during RoundTrip would put a full directory scan in front of
// every request. (solvent-streets-b136)
const (
	// DefaultMaxAge is the default age ceiling. The transport's TTL is
	// 24h, so anything past a month has failed to be requested through
	// ~30 revalidation windows and is almost certainly a dead key.
	DefaultMaxAge = 30 * 24 * time.Hour

	// DefaultMaxSize is the default total-size ceiling. A single city's
	// Overpass + ArcGIS responses run to tens of MiB, so this leaves
	// room for several cities' worth of entries plus revisions while
	// still bounding the directory.
	DefaultMaxSize int64 = 500 << 20

	// orphanGrace is how recently a half-written entry must have been
	// touched to be left alone. writeCache commits the body before the
	// meta, so a concurrent pvmt process legitimately has a `.json`
	// with no `.meta` for the microseconds between the two renames;
	// sweeping that would delete a body out from under the writer.
	// Anything older than the grace is a genuine crash residue. The
	// window only delays reclamation to the next prune.
	orphanGrace = 5 * time.Minute
)

// isTempFile reports whether name is a temp file cmdutil.WriteFile left
// behind after being killed between CreateTemp and rename (its normal
// error paths clean up after themselves). Such a file is cache bytes with
// no reader, so prune reclaims it alongside orphans.
//
// The shape is derived from cmdutil.TempPattern rather than restated as a
// literal here: a hard-coded second copy would let a change to the
// pattern silently stop this sweep from matching anything. The base must
// still be a real entry name, so an unrelated "notes.tmp-1" is not ours.
func isTempFile(name string) bool {
	prefix, suffix, ok := strings.Cut(cmdutil.TempPattern(""), "*")
	if !ok {
		return false
	}
	if !strings.HasSuffix(name, suffix) {
		return false
	}
	rest := strings.TrimSuffix(name, suffix)
	i := strings.LastIndex(rest, prefix)
	if i < 0 {
		return false
	}
	base, random := rest[:i], rest[i+len(prefix):]
	if random == "" || strings.TrimLeft(random, "0123456789") != "" {
		return false
	}
	_, _, ok = splitEntryName(base)
	return ok
}

// PruneOptions configures a sweep. A zero MaxAge disables age pruning and
// a zero MaxSize disables size pruning; orphan and temp-file cleanup runs
// unconditionally, since those files can never be served.
type PruneOptions struct {
	MaxAge  time.Duration
	MaxSize int64
	DryRun  bool
}

// PruneReport summarizes a sweep. With DryRun set the Removed/Reclaimed
// counters describe what would have happened; nothing was written.
type PruneReport struct {
	// EntriesScanned counts distinct cache keys found (a `.json`/`.meta`
	// pair is one entry), including orphans. Temp files are not entries.
	EntriesScanned int
	// EntriesRemoved counts complete entries evicted by age or size.
	EntriesRemoved int
	// AgeRemoved and SizeRemoved break EntriesRemoved down by cause.
	AgeRemoved  int
	SizeRemoved int
	// OrphansRemoved counts half-written entries and leftover temp
	// files reclaimed.
	OrphansRemoved int
	// BytesReclaimed is the on-disk size of everything removed, counting
	// both files of a pair.
	BytesReclaimed int64
	// BytesRemaining is the size of what survives the sweep.
	BytesRemaining int64
	// Skipped counts directory entries prune deliberately left alone:
	// subdirectories, symlinks, and files that do not match the
	// transport's naming scheme.
	Skipped int
}

// cacheFile is one on-disk file with the size prune credits for removing it.
type cacheFile struct {
	path string
	size int64
}

// cacheEntry groups the files sharing a cache key.
type cacheEntry struct {
	files   []cacheFile
	size    int64
	mtime   time.Time
	hasBody bool
	hasMeta bool
}

func (e *cacheEntry) complete() bool { return e.hasBody && e.hasMeta }

// Prune bounds the on-disk HTTP cache in dir. It removes, in order:
// orphaned/half-written entries and leftover temp files; entries whose
// most recent write is older than opts.MaxAge; and then, oldest first,
// whole entries until the total falls to opts.MaxSize.
//
// Age and LRU order both key off mtime rather than the timestamp inside
// the meta, because a 304 revalidation rewrites the meta file (bumping
// its mtime) without touching the body — mtime is therefore the
// last-useful-to-someone signal, and reading every meta would turn a
// directory scan into a full parse of the cache.
//
// A missing dir is not an error (nothing has been cached yet). The sweep
// takes no lock and tolerates another pvmt process mutating the
// directory underneath it: a file that vanishes between the scan and the
// remove is treated as already reclaimed, not as a failure. Removal
// failures that are not "already gone" (e.g. EPERM) are joined into the
// returned error, and the report is still returned so the caller can
// print the partial result.
// Cancellation: ctx is checked once per directory entry and once per entry in
// each sweep, so a SIGINT stops the sweep promptly instead of running to
// completion. Before this, `pvmt cache prune --yes` observed ctx nowhere at
// all, and because signal.NotifyContext keeps the default SIGINT disposition
// disabled for the whole run, NO Ctrl-C terminated it mid-sweep
// (solvent-streets-q48z.22). A cancelled sweep returns a context.Canceled in
// the joined error alongside the partial report — entries it never reached are
// credited to BytesRemaining, so the printed numbers stay honest.
func Prune(ctx context.Context, dir string, opts PruneOptions) (*PruneReport, error) {
	p := &pruneRun{ctx: ctx, opts: opts, report: &PruneReport{}, now: time.Now()}
	entries, temps, err := scanCacheDir(ctx, dir, p.report)
	if err != nil {
		return p.report, err
	}

	live := p.sweepIncomplete(entries, temps)

	// Oldest first: the age pass reads as a prefix of this order and the
	// size pass evicts from the same end (LRU by mtime).
	slices.SortFunc(live, func(a, b *cacheEntry) int { return a.mtime.Compare(b.mtime) })

	kept := p.sweepByAge(live)
	p.sweepBySize(kept)

	return p.report, errors.Join(p.errs...)
}

// pruneRun carries the mutable state of one sweep so the passes don't
// thread four parameters each.
type pruneRun struct {
	ctx    context.Context
	opts   PruneOptions
	report *PruneReport
	now    time.Time
	errs   []error
}

// cancelled reports whether the run should stop, recording the cancellation
// once so errors.Join surfaces it exactly once no matter how many sweeps
// observe it.
func (p *pruneRun) cancelled() bool {
	err := p.ctx.Err()
	if err == nil {
		return false
	}
	if !slices.ContainsFunc(p.errs, func(e error) bool { return errors.Is(e, err) }) {
		p.errs = append(p.errs, err)
	}
	return true
}

// evict removes one entry's files and reports whether the entry is fully
// gone. Counters are the caller's job and must only be incremented when
// this returns true: a partially removed entry (EPERM on one half, a
// read-only mount) would otherwise be reported as "removed: 5 by age"
// directly above "reclaimed: 0 B". Bytes that survive a failure are
// credited to BytesRemaining here, so callers never double-count them.
func (p *pruneRun) evict(e *cacheEntry) bool {
	reclaimed, err := removeEntry(e, p.opts.DryRun)
	p.report.BytesReclaimed += reclaimed
	if err != nil {
		p.errs = append(p.errs, err)
		p.report.BytesRemaining += e.size - reclaimed
		return false
	}
	return true
}

// sweepIncomplete removes what can never be served — half-written pairs
// and leftover temp files — and returns the complete entries.
func (p *pruneRun) sweepIncomplete(entries map[string]*cacheEntry, temps []*cacheEntry) []*cacheEntry {
	live := make([]*cacheEntry, 0, len(entries))
	for _, e := range entries {
		if p.cancelled() {
			// Stop removing, but keep the accounting honest: an entry we
			// never examined survived the sweep, which is exactly what
			// `live` means to the passes downstream.
			live = append(live, e)
			continue
		}
		p.report.EntriesScanned++
		if e.complete() {
			live = append(live, e)
			continue
		}
		p.sweepResidue(e)
	}
	for _, tmp := range temps {
		if p.cancelled() {
			break
		}
		p.sweepResidue(tmp)
	}
	return live
}

// sweepResidue removes one orphan or temp file, unless it is recent
// enough to be another process's in-flight write.
func (p *pruneRun) sweepResidue(e *cacheEntry) {
	if p.now.Sub(e.mtime) < orphanGrace {
		p.report.BytesRemaining += e.size
		return
	}
	if p.evict(e) {
		p.report.OrphansRemoved++
	}
}

// sweepByAge removes entries whose most recent write is past MaxAge and
// returns the survivors, still oldest-first.
func (p *pruneRun) sweepByAge(live []*cacheEntry) []*cacheEntry {
	if p.opts.MaxAge <= 0 {
		return live
	}
	kept := live[:0:0]
	for i, e := range live {
		if p.cancelled() {
			// Everything not yet examined survives, so hand it on: the size
			// pass credits its bytes to BytesRemaining.
			kept = append(kept, live[i:]...)
			break
		}
		if p.now.Sub(e.mtime) <= p.opts.MaxAge {
			kept = append(kept, e)
			continue
		}
		if p.evict(e) {
			p.report.EntriesRemoved++
			p.report.AgeRemoved++
		}
	}
	return kept
}

// sweepBySize evicts oldest-first until the surviving entries fit under
// MaxSize.
func (p *pruneRun) sweepBySize(kept []*cacheEntry) {
	var remaining int64
	for _, e := range kept {
		remaining += e.size
	}
	for _, e := range kept {
		if p.opts.MaxSize <= 0 || remaining <= p.opts.MaxSize {
			break
		}
		// Breaking here leaves every unprocessed entry's size in `remaining`,
		// which is credited to BytesRemaining below — no accounting fixup needed.
		if p.cancelled() {
			break
		}
		// Decrement unconditionally: the entry has been processed either
		// way, and evict() credits any bytes that survived a failure
		// straight to BytesRemaining. A failed eviction can therefore
		// leave the cache over the cap — the returned error says so, and
		// the next sweep retries.
		remaining -= e.size
		if p.evict(e) {
			p.report.EntriesRemoved++
			p.report.SizeRemoved++
		}
	}
	p.report.BytesRemaining += remaining
}

// cacheScan accumulates a directory scan: recognized files grouped by
// cache key, plus leftover temp files modeled as single-file entries.
type cacheScan struct {
	dir     string
	entries map[string]*cacheEntry
	temps   []*cacheEntry
	report  *PruneReport
}

// scanCacheDir reads dir one level deep and groups the recognized files by
// cache key.
func scanCacheDir(ctx context.Context, dir string, report *PruneReport) (map[string]*cacheEntry, []*cacheEntry, error) {
	dirEnts, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Nothing cached yet — an empty sweep, not a failure.
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read cache dir %s: %w", dir, err)
	}

	s := &cacheScan{dir: dir, entries: make(map[string]*cacheEntry), report: report}
	for _, de := range dirEnts {
		if err := ctx.Err(); err != nil {
			// A huge cache dir can take a while to stat; bail out rather than
			// finishing a scan whose sweep is about to be abandoned anyway.
			return nil, nil, err
		}
		s.add(de)
	}
	return s.entries, s.temps, nil
}

// add classifies one directory entry into the scan.
func (s *cacheScan) add(de fs.DirEntry) {
	// ReadDir reports each entry's own type (lstat semantics), so a
	// symlink is ModeSymlink even when it resolves to a regular file.
	// Anything that is not a plain file is skipped: prune never follows a
	// link out of the cache dir, and it never recurses into a
	// subdirectory. The transport writes a strictly flat directory of
	// regular files, so both cases mean "not ours" — deleting through
	// them could reach arbitrary paths.
	if !de.Type().IsRegular() {
		s.report.Skipped++
		return
	}
	info, err := de.Info()
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) { // ErrNotExist: raced away after readdir
			s.report.Skipped++
		}
		return
	}

	name := de.Name()
	file := cacheFile{path: filepath.Join(s.dir, name), size: info.Size()}
	if isTempFile(name) {
		s.temps = append(s.temps, &cacheEntry{
			files: []cacheFile{file},
			size:  file.size,
			mtime: info.ModTime(),
		})
		return
	}
	key, isMeta, ok := splitEntryName(name)
	if !ok {
		// Not written by this transport; leave it untouched.
		s.report.Skipped++
		return
	}
	s.addFile(key, file, isMeta, info.ModTime())
}

// addFile folds one half of a pair into its entry. The entry's mtime is
// the newer of its two files: a 304 revalidation rewrites only the meta,
// and that still counts as the entry being wanted.
func (s *cacheScan) addFile(key string, file cacheFile, isMeta bool, mtime time.Time) {
	e := s.entries[key]
	if e == nil {
		e = &cacheEntry{}
		s.entries[key] = e
	}
	e.files = append(e.files, file)
	e.size += file.size
	if mtime.After(e.mtime) {
		e.mtime = mtime
	}
	if isMeta {
		e.hasMeta = true
	} else {
		e.hasBody = true
	}
}

// splitEntryName recognizes the transport's `<key>.json` / `<key>.meta`
// naming (see cacheKey and writeCache) and reports the key plus whether
// the file is the meta half. The key must be a full sha256 hex digest:
// prune only ever deletes names it can prove the transport minted, so an
// unrelated file a user parked in the cache dir is left alone.
func splitEntryName(name string) (key string, isMeta, ok bool) {
	ext := filepath.Ext(name)
	if ext != ".json" && ext != ".meta" {
		return "", false, false
	}
	key = strings.TrimSuffix(name, ext)
	if !isCacheKey(key) {
		return "", false, false
	}
	return key, ext == ".meta", true
}

func isCacheKey(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// removeEntry deletes every file of an entry and returns the bytes that
// actually went away. A file that is already gone still counts as
// reclaimed — the bytes are off the disk either way, and racing with
// another pruner or with readCache's corrupt-entry cleanup is normal, not
// an error. Bookkeeping beyond the byte count is the caller's (see
// pruneRun.evict), which is what keeps the counters honest when only part
// of a pair could be removed.
func removeEntry(e *cacheEntry, dryRun bool) (int64, error) {
	var (
		reclaimed int64
		errs      []error
	)
	for _, f := range e.files {
		if !dryRun {
			if err := os.Remove(f.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("remove %s: %w", f.path, err))
				continue
			}
		}
		reclaimed += f.size
	}
	return reclaimed, errors.Join(errs...)
}

// ParseSize parses a human-friendly byte size: a decimal integer with an
// optional unit suffix, case-insensitive. Suffixes are binary — "1k" is
// 1024 bytes — and "kb"/"kib" are accepted as aliases of "k". A bare
// number is bytes; "0" means unbounded.
func ParseSize(s string) (int64, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	if t == "" {
		return 0, errors.New("empty size")
	}
	// Leading digit run is the number, the rest is the unit. A leading
	// "-" therefore yields an empty number and is rejected below rather
	// than parsing to a negative cap.
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	num, unit := t[:i], strings.TrimSpace(t[i:])
	if num == "" {
		return 0, fmt.Errorf("invalid size %q (want a number, optionally suffixed: 500mb)", s)
	}
	mult, ok := sizeMultiplier(unit)
	if !ok {
		// This message is where a confused user actually lands, so it
		// spells out the accepted aliases and that the units are binary
		// rather than assuming they read the flag help.
		return 0, fmt.Errorf(
			"invalid size unit %q in %q (want b, k/kb/kib, m/mb/mib, g/gb/gib, or t/tb/tib; units are binary, so 1k = 1024 bytes)",
			unit, s)
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", s, err)
	}
	if mult > 1 && n > math.MaxInt64/mult {
		return 0, fmt.Errorf("size %q overflows int64", s)
	}
	return n * mult, nil
}

func sizeMultiplier(unit string) (int64, bool) {
	switch unit {
	case "", "b":
		return 1, true
	case "k", "kb", "kib":
		return 1 << 10, true
	case "m", "mb", "mib":
		return 1 << 20, true
	case "g", "gb", "gib":
		return 1 << 30, true
	case "t", "tb", "tib":
		return 1 << 40, true
	}
	return 0, false
}
