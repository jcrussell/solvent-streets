package all

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/resource"
	"github.com/jcrussell/solvent-streets/internal/units"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// TestNewCmdAll_RegistersSubcommands pins the thin-wrapper contract:
// the factory returns a cobra.Command exposing the cross-resource
// ingest+compute fan-out. status is intentionally absent — `pvmt status`
// already covers every resource without a per-resource flag, so wiring
// `pvmt all status` would just duplicate it.
func TestNewCmdAll_RegistersSubcommands(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:  ios,
		UnitSystem: func() units.System { return units.Imperial },
	}

	cmd := NewCmdAll(f)
	if cmd == nil {
		t.Fatal("NewCmdAll returned nil")
	}
	if cmd.Use != "all" {
		t.Errorf("Use = %q, want %q", cmd.Use, "all")
	}

	got := map[string]bool{}
	for _, sub := range cmd.Commands() {
		got[sub.Name()] = true
	}
	for _, name := range []string{"ingest", "compute"} {
		if !got[name] {
			t.Errorf("missing %q subcommand", name)
		}
	}
	if got["status"] {
		t.Errorf("unexpected %q subcommand on `all` — `pvmt status` already covers all resources", "status")
	}
}

// TestForEachResource_ErrAllSourcesFailedContinues pins finding 0yfp: a
// total-source-failure for ONE resource must warn and continue the fan-out
// (so parking/sidewalks still run), not abort the whole pass with an early
// return. ErrNoResults is likewise skipped; a generic error also warns and
// continues; only context.Canceled aborts.
func TestForEachResource_ErrAllSourcesFailedContinues(t *testing.T) {
	t.Run("ErrAllSourcesFailed warns and continues", func(t *testing.T) {
		ios, _, _, errBuf := iostreams.Test()
		var visited []resource.Type
		err := forEachResource(ios, func(rt resource.Source) error {
			visited = append(visited, rt.Type())
			if rt.Type() == resource.All[0].Type() {
				return cmdutil.ErrAllSourcesFailed
			}
			return nil
		})
		if err != nil {
			t.Fatalf("forEachResource returned %v; want nil (non-fatal)", err)
		}
		if len(visited) != len(resource.All) {
			t.Errorf("visited %d resources; want all %d (first failure must not abort)", len(visited), len(resource.All))
		}
		if !strings.Contains(errBuf.String(), "failed") {
			t.Errorf("expected a warning on ErrOut, got %q", errBuf.String())
		}
	})

	t.Run("context.Canceled aborts", func(t *testing.T) {
		ios, _, _, _ := iostreams.Test()
		var visited int
		err := forEachResource(ios, func(_ resource.Source) error {
			visited++
			return context.Canceled
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
		if visited != 1 {
			t.Errorf("cancellation must abort after the first resource; visited %d", visited)
		}
	})
}
