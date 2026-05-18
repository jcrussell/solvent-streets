package export

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

func TestNewCmdExport_RunFInjection(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}

	var gotOpts *Options
	cmd := NewCmdExport(f, func(opts *Options) error {
		gotOpts = opts
		return nil
	})
	cmd.SetArgs([]string{"-o", "/tmp/site", "--clean"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotOpts == nil {
		t.Fatal("runF was not invoked")
	}
	if gotOpts.OutputDir != "/tmp/site" {
		t.Errorf("expected OutputDir /tmp/site, got %q", gotOpts.OutputDir)
	}
	if !gotOpts.Clean {
		t.Errorf("expected --clean to be true")
	}
}

func TestNewCmdExport_Defaults(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}

	var gotOpts *Options
	cmd := NewCmdExport(f, func(opts *Options) error {
		gotOpts = opts
		return nil
	})
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotOpts.OutputDir != "dist" {
		t.Errorf("expected default OutputDir dist, got %q", gotOpts.OutputDir)
	}
	if gotOpts.Clean {
		t.Errorf("expected default --clean to be false")
	}
}

// TestOptions_Validate_EmptyOutputDir locks in byob-input-validation.5:
// an empty --output value fails at the Options boundary, not deep inside
// the exporter, and surfaces as *cmdutil.FlagError so the top-level
// runner maps it to exit code 2.
func TestOptions_Validate_EmptyOutputDir(t *testing.T) {
	opts := &Options{OutputDir: ""}
	err := opts.Validate()
	if err == nil {
		t.Fatal("expected error for empty --output, got nil")
	}
	var flag *cmdutil.FlagError
	if !errors.As(err, &flag) {
		t.Errorf("error %v is not *FlagError", err)
	}
}

// TestOptions_Validate_SensitiveLocations locks in the byob-input-validation.5
// rejection of paths that would erase user data on --clean (the filesystem
// root and the user's home directory). The cmdutil.ResolveOutputDir helper
// owns this list; this test pins the integration through Options.Validate.
func TestOptions_Validate_SensitiveLocations(t *testing.T) {
	cases := []struct {
		name string
		dir  func(t *testing.T) string
	}{
		{"root", func(*testing.T) string { return "/" }},
		{"home", func(t *testing.T) string {
			t.Helper()
			home, err := os.UserHomeDir()
			if err != nil || home == "" {
				t.Skipf("no home dir: %v", err)
			}
			return home
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := &Options{OutputDir: tc.dir(t), Clean: true}
			err := opts.Validate()
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			var flag *cmdutil.FlagError
			if !errors.As(err, &flag) {
				t.Errorf("error %v is not *FlagError", err)
			}
		})
	}
}

// TestOptions_Validate_ExistingWithoutClean covers the safety check that
// stops Export from blowing away a populated directory the user didn't
// authorize. The same condition is re-checked just before os.RemoveAll
// in runExport, but the boundary check is what makes the failure cheap
// (no DB open, no compute) and FlagError-shaped.
func TestOptions_Validate_ExistingWithoutClean(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "site")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	opts := &Options{OutputDir: dir, Clean: false}
	err := opts.Validate()
	if err == nil {
		t.Fatal("expected error for existing dir without --clean, got nil")
	}
	var flag *cmdutil.FlagError
	if !errors.As(err, &flag) {
		t.Errorf("error %v is not *FlagError", err)
	}
}

// TestOptions_Validate_ExistingWithClean accepts the same existing dir
// once --clean is passed. The Validate call also canonicalises OutputDir
// (abs-resolved, symlinks followed) — assert that the resolved value
// roundtrips through the field so downstream code reads the resolved
// path, not the raw flag value.
func TestOptions_Validate_ExistingWithClean(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "site")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	opts := &Options{OutputDir: dir, Clean: true}
	if err := opts.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !filepath.IsAbs(opts.OutputDir) {
		t.Errorf("OutputDir not resolved to abs: %q", opts.OutputDir)
	}
}

// TestOptions_Validate_NonExistentInWritableParent accepts the common
// case: an output dir that doesn't exist yet but whose parent is
// writable. This is the path runExport takes when there's nothing to
// remove.
func TestOptions_Validate_NonExistentInWritableParent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh-site")
	opts := &Options{OutputDir: dir, Clean: false}
	if err := opts.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
