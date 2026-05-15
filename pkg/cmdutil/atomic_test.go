package cmdutil_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
)

func TestWriteFile_CreatesFileWithContent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	want := []byte("hello world")

	if err := cmdutil.WriteFile(target, want, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("contents = %q, want %q", got, want)
	}
}

func TestWriteFile_RespectsPerm(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "perm.txt")

	if err := cmdutil.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestWriteFile_OverwritesAtomically(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "overwrite.txt")

	if err := cmdutil.WriteFile(target, []byte("first"), 0o644); err != nil {
		t.Fatalf("first WriteFile: %v", err)
	}
	if err := cmdutil.WriteFile(target, []byte("second"), 0o644); err != nil {
		t.Fatalf("second WriteFile: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "second" {
		t.Errorf("contents = %q, want %q", got, "second")
	}

	// No leftover *.tmp-* siblings.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "overwrite.txt" {
			t.Errorf("unexpected leftover %q", e.Name())
		}
	}
}

func TestWriteFile_NoLeftoverOnRenameFailure(t *testing.T) {
	dir := t.TempDir()
	conflictDir := filepath.Join(dir, "conflict")
	if err := os.Mkdir(conflictDir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	err := cmdutil.WriteFile(conflictDir, []byte("x"), 0o644)
	if err == nil {
		t.Fatal("expected error renaming over an existing directory")
	}

	// Temp files should be cleaned up.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() == "conflict" {
			continue
		}
		t.Errorf("unexpected leftover %q after failed rename", e.Name())
	}
}

func TestWriteFile_MissingParent(t *testing.T) {
	target := filepath.Join(t.TempDir(), "does-not-exist", "file.txt")
	err := cmdutil.WriteFile(target, []byte("x"), 0o644)
	if err == nil {
		t.Fatal("expected error for missing parent")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error = %v, want fs.ErrNotExist", err)
	}
}
