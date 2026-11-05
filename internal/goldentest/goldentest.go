// Package goldentest compares test output against a committed golden file
// without ever rewriting it. There is deliberately no -update flag: on a
// mismatch the test fails with a diff, writes the actual output under the
// repository's gitignored bin/ directory, and prints the `cp -f` that would
// accept it, so a person reads the diff before the golden moves
// (byob-testing.8).
package goldentest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// Check fails tb unless got is byte-identical to the golden at goldenPath
// (relative to the test's working directory, i.e. its package directory).
func Check(tb testing.TB, goldenPath string, got []byte) {
	tb.Helper()
	want, err := os.ReadFile(goldenPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		tb.Fatalf("read golden %s: %v", goldenPath, err)
	}
	if err == nil && string(want) == string(got) {
		return
	}

	root, rerr := repoRoot()
	if rerr != nil {
		tb.Fatalf("golden %s mismatch, and locating the repo root failed: %v", goldenPath, rerr)
	}
	absGolden, aerr := filepath.Abs(goldenPath)
	if aerr != nil {
		tb.Fatalf("abs %s: %v", goldenPath, aerr)
	}
	relGolden, rerr := filepath.Rel(root, absGolden)
	if rerr != nil {
		tb.Fatalf("rel %s: %v", absGolden, rerr)
	}
	actual := filepath.Join("bin", "golden", relGolden)
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(actual)), 0o755); err != nil {
		tb.Fatalf("mkdir for actual output: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, actual), got, 0o644); err != nil {
		tb.Fatalf("write actual output: %v", err)
	}

	if want == nil {
		tb.Fatalf("golden %s does not exist. Review the actual output, then from the repo root:\n  cp -f %s %s",
			relGolden, actual, relGolden)
	}
	tb.Fatalf("output differs from golden %s (-want +got):\n%s\nIf the change is intended, review the diff, then from the repo root:\n  cp -f %s %s",
		relGolden, cmp.Diff(string(want), string(got)), actual, relGolden)
}

// repoRoot walks up from the working directory to the directory holding
// go.mod.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod above the working directory")
		}
		dir = parent
	}
}
