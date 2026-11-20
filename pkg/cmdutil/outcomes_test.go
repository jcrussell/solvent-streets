package cmdutil

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/internal/hint"
)

func TestOutcomes(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name    string
		results []error
		want    error // matched with errors.Is; nil means no error
	}{
		{"nothing ran", nil, nil},
		{"all produced", []error{nil, nil}, nil},
		{"one empty among results", []error{nil, ErrNoResults}, nil},
		{"every item empty", []error{ErrNoResults, ErrNoResults}, ErrNoResults},
		{"a failure outranks empties", []error{ErrNoResults, boom}, boom},
		{"a failure is kept beside results", []error{nil, boom}, boom},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out Outcomes
			for _, err := range tt.results {
				out.Add("roads", err)
			}
			got := out.Err()
			if tt.want == nil && got != nil || tt.want != nil && !errors.Is(got, tt.want) {
				t.Fatalf("Err() = %v, want %v", got, tt.want)
			}
			if !errors.Is(tt.want, ErrNoResults) && errors.Is(got, ErrNoResults) {
				t.Errorf("Err() = %v reports no results alongside a failure", got)
			}
		})
	}

	var out Outcomes
	out.Add("roads", boom)
	out.Add("parking", errors.New("parking: all sources failed"))
	if got, want := out.Err().Error(), "roads: boom\nparking: all sources failed"; got != want {
		t.Errorf("labels: got %q, want %q", got, want)
	}
}

// TestOnlyPrereqs: a missing prerequisite exits 3 only when every failure is
// one, however the errors are wrapped and joined.
func TestOnlyPrereqs(t *testing.T) {
	prereq := NeedsFirst(errors.New("no boundary stored"), "pvmt all ingest")
	failure := errors.New("connection refused")
	for name, c := range map[string]struct {
		err  error
		want bool
	}{
		"prereq":                 {prereq, true},
		"wrapped prereq":         {fmt.Errorf("oakland: %w", prereq), true},
		"join of prereqs":        {errors.Join(prereq, fmt.Errorf("b: %w", prereq)), true},
		"join with a failure":    {errors.Join(prereq, failure), false},
		"wrapped mixed join":     {fmt.Errorf("city: %w", errors.Join(prereq, failure)), false},
		"wrapped join of prereq": {fmt.Errorf("city: %w", errors.Join(prereq, prereq)), true},
		"plain failure":          {failure, false},
	} {
		if got := OnlyPrereqs(c.err); got != c.want {
			t.Errorf("%s: OnlyPrereqs = %v, want %v", name, got, c.want)
		}
	}
}

// TestOutcomes_PrereqBesideAResult: a city stopped for want of an earlier
// step fails the run, but only a run in which nothing produced a result is
// "nothing to do" (exit 3); beside a city that did its work it is exit 1.
func TestOutcomes_PrereqBesideAResult(t *testing.T) {
	prereq := NeedsFirst(errors.New("no boundary stored for Beta"), "pvmt --city beta all ingest")

	var alone Outcomes
	alone.Add("Beta", prereq)
	if err := alone.Err(); !OnlyPrereqs(err) {
		t.Errorf("only a prerequisite failed: OnlyPrereqs(%v) = false, want exit 3", err)
	}

	var mixed Outcomes
	mixed.Add("Alpha", nil)
	mixed.Add("Beta", prereq)
	err := mixed.Err()
	if err == nil || OnlyPrereqs(err) {
		t.Fatalf("err = %v, OnlyPrereqs true; want a failure that exits 1", err)
	}
	var h *hint.ErrHint
	if !errors.As(err, &h) || !strings.Contains(err.Error(), "no boundary stored for Beta") {
		t.Errorf("err = %v; want the skipped city reported with its hint", err)
	}
}
