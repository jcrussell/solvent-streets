package cmdutil

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/db"
	"github.com/jcrussell/solvent-streets/internal/paths"

	"github.com/spf13/cobra"
)

func TestSnapshotNameCompletion_NoDatabaseCreatesNone(t *testing.T) {
	dir := t.TempDir()
	opened := false
	f := &Factory{
		Paths:  func() (*paths.Paths, error) { return &paths.Paths{Data: dir}, nil },
		RootDB: func() (*db.RootStore, error) { opened = true; return nil, errors.New("opened") },
	}
	got, dir2 := SnapshotNameCompletion(f, 1)(&cobra.Command{}, nil, "")
	if len(got) != 0 || dir2 != cobra.ShellCompDirectiveNoFileComp || opened {
		t.Errorf("got %v %v (opened %v); want no completions, no files, no database opened", got, dir2, opened)
	}
}

func TestSnapshotNameCompletion_ListsNames(t *testing.T) {
	dir := t.TempDir()
	store, err := db.Open(context.Background(), filepath.Join(dir, db.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f := &Factory{
		Paths:  func() (*paths.Paths, error) { return &paths.Paths{Data: dir}, nil },
		RootDB: func() (*db.RootStore, error) { return store, nil },
	}
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	if got, d := SnapshotNameCompletion(f, 1)(cmd, nil, ""); len(got) != 0 || d&cobra.ShellCompDirectiveNoFileComp == 0 {
		t.Errorf("empty database: got %v %v", got, d)
	}
	if got, _ := SnapshotNameCompletion(f, 1)(cmd, []string{"2026-11"}, ""); len(got) != 0 {
		t.Errorf("past maxArgs: got %v, want none", got)
	}
}

func TestDefaultNoFileCompletion(t *testing.T) {
	root := &cobra.Command{Use: "pvmt"}
	leaf := &cobra.Command{Use: "gc", Run: func(*cobra.Command, []string) {}}
	own := &cobra.Command{Use: "check-site", Run: func(*cobra.Command, []string) {},
		ValidArgsFunction: func(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
			return nil, cobra.ShellCompDirectiveFilterDirs
		}}
	root.AddCommand(leaf, own)
	DefaultNoFileCompletion(root)
	if _, d := leaf.ValidArgsFunction(leaf, nil, ""); d != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("gc: directive %v, want NoFileComp", d)
	}
	if _, d := own.ValidArgsFunction(own, nil, ""); d != cobra.ShellCompDirectiveFilterDirs {
		t.Errorf("check-site: directive %v, want its own FilterDirs", d)
	}
}
