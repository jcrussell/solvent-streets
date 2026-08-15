package cache

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
)

// key returns a well-formed cache key (64 lowercase hex chars) derived
// from n, matching what cacheKey produces.
func key(n int) string { return fmt.Sprintf("%064x", n) }

// writeEntry lays down a complete `<key>.json` / `<key>.meta` pair with
// the given per-file byte count and mtime, and returns the two paths.
func writeEntry(t *testing.T, dir, k string, size int, age time.Duration) (string, string) {
	t.Helper()
	body := filepath.Join(dir, k+".json")
	meta := filepath.Join(dir, k+".meta")
	writeFileAged(t, body, size, age)
	writeFileAged(t, meta, size, age)
	return body, meta
}

func writeFileAged(t *testing.T, path string, size int, age time.Duration) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

// writeEntryMtimes lays down a pair whose two halves have different
// mtimes, which is the shape a 304 revalidation produces.
func writeEntryMtimes(t *testing.T, dir, k string, bodyAge, metaAge time.Duration) (string, string) {
	t.Helper()
	body := filepath.Join(dir, k+".json")
	meta := filepath.Join(dir, k+".meta")
	writeFileAged(t, body, 100, bodyAge)
	writeFileAged(t, meta, 100, metaAge)
	return body, meta
}

// leftoverTempFile creates the residue cmdutil.WriteFile leaves when it is
// killed between CreateTemp and rename, using the very same pattern +
// os.CreateTemp pair WriteFile uses. Building it any other way (a
// hand-written ".tmp-123" literal) would let atomic.go change its naming
// while this test kept passing against a stale assumption.
//
// WriteFile itself cannot be driven into leaving one behind: every error
// path removes the temp, and only a SIGKILL produces the real thing.
func leftoverTempFile(t *testing.T, dir, base string, age time.Duration) string {
	t.Helper()
	f, err := os.CreateTemp(dir, cmdutil.TempPattern(base))
	if err != nil {
		t.Fatal(err)
	}
	name := f.Name()
	if _, err := f.Write(make([]byte, 50)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(name, when, when); err != nil {
		t.Fatal(err)
	}
	return name
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

// TestPrune_AgeRemovesStaleEntriesOnly is the age half of b136: an entry
// untouched for longer than MaxAge goes, a recent one stays, and both
// files of the removed pair are unlinked (not just the body).
func TestPrune_AgeRemovesStaleEntriesOnly(t *testing.T) {
	dir := t.TempDir()
	oldBody, oldMeta := writeEntry(t, dir, key(1), 100, 48*time.Hour)
	newBody, newMeta := writeEntry(t, dir, key(2), 100, time.Hour)

	report, err := Prune(dir, PruneOptions{MaxAge: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	if exists(t, oldBody) || exists(t, oldMeta) {
		t.Error("stale entry survived: both .json and .meta must be removed")
	}
	if !exists(t, newBody) || !exists(t, newMeta) {
		t.Error("fresh entry was removed")
	}
	if report.AgeRemoved != 1 || report.EntriesRemoved != 1 {
		t.Errorf("AgeRemoved=%d EntriesRemoved=%d, want 1/1", report.AgeRemoved, report.EntriesRemoved)
	}
	if report.EntriesScanned != 2 {
		t.Errorf("EntriesScanned = %d, want 2 (a pair is one entry)", report.EntriesScanned)
	}
	// Both files of the pair count toward the reclaimed total.
	if report.BytesReclaimed != 200 {
		t.Errorf("BytesReclaimed = %d, want 200", report.BytesReclaimed)
	}
	if report.BytesRemaining != 200 {
		t.Errorf("BytesRemaining = %d, want 200", report.BytesRemaining)
	}
}

// TestPrune_MaxAgeZeroDisablesAgePruning pins the documented escape hatch.
func TestPrune_MaxAgeZeroDisablesAgePruning(t *testing.T) {
	dir := t.TempDir()
	body, meta := writeEntry(t, dir, key(1), 10, 10*365*24*time.Hour)

	report, err := Prune(dir, PruneOptions{MaxAge: 0, MaxSize: 0})
	if err != nil {
		t.Fatal(err)
	}
	if !exists(t, body) || !exists(t, meta) {
		t.Error("MaxAge=0 must not prune by age")
	}
	if report.EntriesRemoved != 0 {
		t.Errorf("EntriesRemoved = %d, want 0", report.EntriesRemoved)
	}
}

// TestPrune_SizeEvictsOldestFirst is the size half of b136: over the cap,
// entries go in mtime order (LRU) until the total fits, and no further.
func TestPrune_SizeEvictsOldestFirst(t *testing.T) {
	dir := t.TempDir()
	// Each entry is 200 bytes on disk (2 files x 100). Oldest first.
	oldestBody, _ := writeEntry(t, dir, key(1), 100, 3*time.Hour)
	middleBody, _ := writeEntry(t, dir, key(2), 100, 2*time.Hour)
	newestBody, newestMeta := writeEntry(t, dir, key(3), 100, time.Hour)

	// 600 on disk, cap at 250 -> must drop the two oldest (leaving 200).
	report, err := Prune(dir, PruneOptions{MaxSize: 250})
	if err != nil {
		t.Fatal(err)
	}

	if exists(t, oldestBody) {
		t.Error("oldest entry survived eviction")
	}
	if exists(t, middleBody) {
		t.Error("middle entry survived eviction")
	}
	if !exists(t, newestBody) || !exists(t, newestMeta) {
		t.Error("newest entry was evicted; eviction must stop once under the cap")
	}
	if report.SizeRemoved != 2 {
		t.Errorf("SizeRemoved = %d, want 2", report.SizeRemoved)
	}
	if report.BytesReclaimed != 400 || report.BytesRemaining != 200 {
		t.Errorf("reclaimed=%d remaining=%d, want 400/200", report.BytesReclaimed, report.BytesRemaining)
	}
}

// TestPrune_SizeCountsBothFilesOfAPair guards the easy bug: sizing off
// only the .json half would under-count and leave the cache over the cap.
func TestPrune_SizeCountsBothFilesOfAPair(t *testing.T) {
	dir := t.TempDir()
	body, meta := writeEntry(t, dir, key(1), 100, time.Hour)

	// 200 on disk. A cap of 150 is under the true size but over the
	// body-only size, so a half-counting implementation would keep it.
	report, err := Prune(dir, PruneOptions{MaxSize: 150})
	if err != nil {
		t.Fatal(err)
	}
	if exists(t, body) || exists(t, meta) {
		t.Error("entry survived: the .meta bytes must count toward the size total")
	}
	if report.BytesReclaimed != 200 {
		t.Errorf("BytesReclaimed = %d, want 200", report.BytesReclaimed)
	}
}

// TestPrune_SweepsOrphansAndTempFiles covers the crash residue the narrow
// readCache cleanup structurally cannot reach: a body with no meta (killed
// between writeCache's two renames), a meta with no body, and a leftover
// cmdutil.WriteFile temp file.
func TestPrune_SweepsOrphansAndTempFiles(t *testing.T) {
	dir := t.TempDir()
	bodyOnly := filepath.Join(dir, key(1)+".json")
	metaOnly := filepath.Join(dir, key(2)+".meta")
	writeFileAged(t, bodyOnly, 50, time.Hour)
	writeFileAged(t, metaOnly, 50, time.Hour)
	tempFile := leftoverTempFile(t, dir, key(3)+".json", time.Hour)
	intactBody, intactMeta := writeEntry(t, dir, key(4), 10, time.Hour)

	// No age or size pressure: orphan cleanup is unconditional.
	report, err := Prune(dir, PruneOptions{MaxAge: 0, MaxSize: 0})
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{bodyOnly, metaOnly, tempFile} {
		if exists(t, p) {
			t.Errorf("%s survived the orphan sweep", filepath.Base(p))
		}
	}
	if !exists(t, intactBody) || !exists(t, intactMeta) {
		t.Error("a complete entry was swept as an orphan")
	}
	if report.OrphansRemoved != 3 {
		t.Errorf("OrphansRemoved = %d, want 3", report.OrphansRemoved)
	}
	if report.EntriesRemoved != 0 {
		t.Errorf("EntriesRemoved = %d, want 0 (orphans are counted separately)", report.EntriesRemoved)
	}
	if report.BytesReclaimed != 150 {
		t.Errorf("BytesReclaimed = %d, want 150", report.BytesReclaimed)
	}
}

// TestPrune_EntryAgeIsTheNewerHalf is the load-bearing decision of the
// whole design: an entry's age is max(body mtime, meta mtime), not the
// body's. A 304 revalidation rewrites ONLY the .meta — the body keeps its
// original mtime forever — so keying off the body (or off whichever half
// the directory scan happened to see first) would evict entries that were
// confirmed fresh minutes ago and force a full re-download.
func TestPrune_EntryAgeIsTheNewerHalf(t *testing.T) {
	tests := []struct {
		name             string
		bodyAge, metaAge time.Duration
	}{
		// The 304 case: old body, meta refreshed an hour ago. ReadDir
		// returns ".json" before ".meta", so a first-wins implementation
		// takes the stale body mtime here.
		{"revalidated meta is newer", 90 * 24 * time.Hour, time.Hour},
		// The converse ordering, so the test cannot pass by accident on
		// directory iteration order alone.
		{"body is newer", time.Hour, 90 * 24 * time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			body, meta := writeEntryMtimes(t, dir, key(1), tc.bodyAge, tc.metaAge)

			report, err := Prune(dir, PruneOptions{MaxAge: 24 * time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			if !exists(t, body) || !exists(t, meta) {
				t.Error("entry was evicted; its age must be the NEWER of the two halves")
			}
			if report.AgeRemoved != 0 {
				t.Errorf("AgeRemoved = %d, want 0", report.AgeRemoved)
			}
		})
	}
}

// TestPrune_EntryAgeUsesNewerHalfForLRU pins the same rule on the size
// pass: a recently revalidated entry must outrank an entry whose halves
// are both older, regardless of body mtime.
func TestPrune_EntryAgeUsesNewerHalfForLRU(t *testing.T) {
	dir := t.TempDir()
	// Revalidated: ancient body, meta touched an hour ago.
	hotBody, hotMeta := writeEntryMtimes(t, dir, key(1), 90*24*time.Hour, time.Hour)
	// Genuinely cold: both halves a day old.
	coldBody, coldMeta := writeEntryMtimes(t, dir, key(2), 24*time.Hour, 24*time.Hour)

	// 400 bytes on disk, cap 250 -> exactly one entry must go.
	if _, err := Prune(dir, PruneOptions{MaxSize: 250}); err != nil {
		t.Fatal(err)
	}

	if !exists(t, hotBody) || !exists(t, hotMeta) {
		t.Error("the revalidated entry was evicted before the colder one")
	}
	if exists(t, coldBody) || exists(t, coldMeta) {
		t.Error("the colder entry survived; LRU order must use the newer half")
	}
}

// TestPrune_SweepsRealWriteFileResidue closes the loop with the writer:
// the residue is created through cmdutil.TempPattern (what WriteFile
// passes to os.CreateTemp), and a *successful* WriteFile must leave
// nothing behind for prune to find.
func TestPrune_SweepsRealWriteFileResidue(t *testing.T) {
	dir := t.TempDir()
	residue := leftoverTempFile(t, dir, key(1)+".meta", time.Hour)

	// A completed write leaves exactly the pair and no temp file.
	if err := cmdutil.WriteFile(filepath.Join(dir, key(2)+".json"), make([]byte, 10), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdutil.WriteFile(filepath.Join(dir, key(2)+".meta"), make([]byte, 10), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := Prune(dir, PruneOptions{MaxAge: 0, MaxSize: 0})
	if err != nil {
		t.Fatal(err)
	}
	if exists(t, residue) {
		t.Errorf("%s survived; prune no longer recognizes cmdutil.WriteFile's temp naming",
			filepath.Base(residue))
	}
	if report.OrphansRemoved != 1 {
		t.Errorf("OrphansRemoved = %d, want 1", report.OrphansRemoved)
	}
	// The completed pair must read as one ordinary entry, not as residue.
	if report.EntriesScanned != 1 {
		t.Errorf("EntriesScanned = %d, want 1", report.EntriesScanned)
	}
	if report.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0 — WriteFile's output must be recognized", report.Skipped)
	}
}

// TestIsTempFile_RejectsForeignNames keeps the temp sweep from widening
// into "anything with .tmp- in it".
func TestIsTempFile_RejectsForeignNames(t *testing.T) {
	cases := map[string]bool{
		key(1) + ".json.tmp-12345": true,
		key(1) + ".meta.tmp-1":     true,
		key(1) + ".json.tmp-":      false, // no random part
		key(1) + ".json.tmp-abc":   false, // CreateTemp only emits digits
		key(1) + ".json":           false, // a real entry half
		"notes.tmp-12345":          false, // not an entry base
		"short.json.tmp-12345":     false, // base is not a cache key
	}
	for name, want := range cases {
		if got := isTempFile(name); got != want {
			t.Errorf("isTempFile(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestPrune_LeavesInFlightOrphansAlone guards the concurrency hazard in
// the orphan sweep: writeCache commits the body before the meta, so a
// just-written body with no meta yet may belong to another live pvmt
// process. Only residue older than orphanGrace is swept.
func TestPrune_LeavesInFlightOrphansAlone(t *testing.T) {
	dir := t.TempDir()
	inFlight := filepath.Join(dir, key(1)+".json")
	writeFileAged(t, inFlight, 50, 0)

	report, err := Prune(dir, PruneOptions{MaxAge: time.Nanosecond, MaxSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !exists(t, inFlight) {
		t.Error("a body written moments ago was swept; a concurrent writer would lose it")
	}
	if report.OrphansRemoved != 0 {
		t.Errorf("OrphansRemoved = %d, want 0", report.OrphansRemoved)
	}
	if report.BytesRemaining != 50 {
		t.Errorf("BytesRemaining = %d, want 50 (in-flight bytes are still on disk)", report.BytesRemaining)
	}
}

// TestPrune_DryRunMakesNoChanges: the report must describe a real sweep
// while the directory is left byte-for-byte intact.
func TestPrune_DryRunMakesNoChanges(t *testing.T) {
	dir := t.TempDir()
	staleBody, staleMeta := writeEntry(t, dir, key(1), 100, 48*time.Hour)
	orphan := filepath.Join(dir, key(2)+".json")
	writeFileAged(t, orphan, 50, time.Hour)

	report, err := Prune(dir, PruneOptions{MaxAge: 24 * time.Hour, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{staleBody, staleMeta, orphan} {
		if !exists(t, p) {
			t.Errorf("--dry-run deleted %s", filepath.Base(p))
		}
	}
	if report.AgeRemoved != 1 || report.OrphansRemoved != 1 {
		t.Errorf("AgeRemoved=%d OrphansRemoved=%d, want 1/1", report.AgeRemoved, report.OrphansRemoved)
	}
	if report.BytesReclaimed != 250 {
		t.Errorf("BytesReclaimed = %d, want 250", report.BytesReclaimed)
	}
}

// TestRemoveEntry_VanishedFileIsNotAnError pins the concurrency contract
// directly: another pvmt process (or readCache's corrupt-entry cleanup)
// may unlink a file between the scan and the remove. That is normal.
func TestRemoveEntry_VanishedFileIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	e := &cacheEntry{files: []cacheFile{
		{path: filepath.Join(dir, key(1)+".json"), size: 100},
		{path: filepath.Join(dir, key(1)+".meta"), size: 20},
	}}
	reclaimed, err := removeEntry(e, false)
	if err != nil {
		t.Fatalf("removing already-gone files must not error: %v", err)
	}
	if reclaimed != 120 {
		t.Errorf("reclaimed = %d, want 120", reclaimed)
	}
}

// TestPrune_FailedRemovalDoesNotCountAsRemoved guards the report's
// honesty: counters must track what actually went, not what was
// attempted, so a read-only cache dir can never print "removed: 1 by
// age" above "reclaimed: 0 B".
func TestPrune_FailedRemovalDoesNotCountAsRemoved(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permissions")
	}
	dir := t.TempDir()
	writeEntry(t, dir, key(1), 100, 48*time.Hour)

	// Read-only directory: the files stat fine but unlink is denied.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	report, err := Prune(dir, PruneOptions{MaxAge: 24 * time.Hour})
	if err == nil {
		t.Fatal("expected the permission failure to be reported")
	}
	if report.AgeRemoved != 0 || report.EntriesRemoved != 0 {
		t.Errorf("AgeRemoved=%d EntriesRemoved=%d, want 0/0 — nothing was actually removed",
			report.AgeRemoved, report.EntriesRemoved)
	}
	if report.BytesReclaimed != 0 {
		t.Errorf("BytesReclaimed = %d, want 0", report.BytesReclaimed)
	}
	if report.BytesRemaining != 200 {
		t.Errorf("BytesRemaining = %d, want 200 (the bytes are still on disk)", report.BytesRemaining)
	}
}

// TestPrune_ConcurrentSweepsDoNotError exercises the same race through the
// full sweep: several pruners racing over one directory all unlink the
// same victims, so most calls lose the race on most files.
func TestPrune_ConcurrentSweepsDoNotError(t *testing.T) {
	dir := t.TempDir()
	for i := range 40 {
		writeEntry(t, dir, key(i), 100, 48*time.Hour)
	}

	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Go(func() {
			_, errs[i] = Prune(dir, PruneOptions{MaxAge: time.Hour})
		})
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent prune %d errored: %v", i, err)
		}
	}
	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("%d files survived concurrent pruning, want 0", len(left))
	}
}

// TestPrune_SkipsForeignPathsAndDoesNotRecurse documents the containment
// rule: prune only touches names it can prove the transport minted, never
// descends into a subdirectory, and never follows a symlink out of the
// cache dir.
func TestPrune_SkipsForeignPathsAndDoesNotRecurse(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()

	// A file outside the cache, reachable only through a symlink whose
	// name looks exactly like a stale cache entry.
	victim := filepath.Join(outside, "precious.json")
	writeFileAged(t, victim, 10, 90*24*time.Hour)
	link := filepath.Join(dir, key(1)+".json")
	if err := os.Symlink(victim, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// A subdirectory whose contents look like stale entries.
	sub := filepath.Join(dir, "nested")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(sub, key(2)+".json")
	writeFileAged(t, nested, 10, 90*24*time.Hour)

	// Files in the cache dir that pvmt did not write.
	notes := filepath.Join(dir, "notes.txt")
	shortKey := filepath.Join(dir, "abc.json")
	writeFileAged(t, notes, 10, 90*24*time.Hour)
	writeFileAged(t, shortKey, 10, 90*24*time.Hour)

	report, err := Prune(dir, PruneOptions{MaxAge: time.Hour, MaxSize: 1})
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{victim, link, nested, notes, shortKey} {
		if _, statErr := os.Lstat(p); statErr != nil {
			t.Errorf("prune removed %s, which it must leave alone: %v", p, statErr)
		}
	}
	// symlink + subdir + notes.txt + abc.json
	if report.Skipped != 4 {
		t.Errorf("Skipped = %d, want 4", report.Skipped)
	}
	if report.EntriesScanned != 0 || report.EntriesRemoved != 0 || report.OrphansRemoved != 0 {
		t.Errorf("unexpected activity: %+v", report)
	}
}

// TestPrune_MissingDirIsNotAnError: pruning before anything was ever
// cached is a no-op, not a failure.
func TestPrune_MissingDirIsNotAnError(t *testing.T) {
	report, err := Prune(filepath.Join(t.TempDir(), "never-created"), PruneOptions{MaxAge: time.Hour})
	if err != nil {
		t.Fatalf("missing cache dir must not error: %v", err)
	}
	if report.EntriesScanned != 0 {
		t.Errorf("EntriesScanned = %d, want 0", report.EntriesScanned)
	}
}

// TestPrune_RealTransportEntriesAreRecognized ties the prune naming rules
// back to the writer: entries produced by writeCache (not hand-built
// fixtures) must be seen as complete pairs and evicted as a unit.
func TestPrune_RealTransportEntriesAreRecognized(t *testing.T) {
	dir := t.TempDir()
	tr := NewTransport(nil, dir, time.Hour)
	k := cacheKey(http.MethodGet, "https://example.com/x", nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Etag": {`"v1"`}}}
	tr.writeCache(
		filepath.Join(dir, k+".meta"),
		filepath.Join(dir, k+".json"),
		resp, []byte("body bytes"), "https://example.com/x",
	)

	report, err := Prune(dir, PruneOptions{MaxSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if report.EntriesScanned != 1 {
		t.Fatalf("EntriesScanned = %d, want 1 (writeCache output must be recognized)", report.EntriesScanned)
	}
	if report.SizeRemoved != 1 {
		t.Errorf("SizeRemoved = %d, want 1", report.SizeRemoved)
	}
	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("%d files left, want 0", len(left))
	}
}

func TestParseSize(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{in: "0", want: 0},
		{in: "512", want: 512},
		{in: "512b", want: 512},
		{in: "1k", want: 1024},
		{in: "1kb", want: 1024},
		{in: "1KiB", want: 1024},
		{in: "500mb", want: 500 << 20},
		{in: " 2 G ", want: 2 << 30},
		{in: "1t", want: 1 << 40},
		{in: "", wantErr: true},
		{in: "mb", wantErr: true},
		{in: "-5", wantErr: true},
		{in: "1.5g", wantErr: true},
		{in: "10x", wantErr: true},
		{in: "9223372036854775807g", wantErr: true}, // overflows
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseSize(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseSize(%q) = %d, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSize(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseSize(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
