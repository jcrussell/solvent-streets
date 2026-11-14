package cmdutil

import (
	"errors"
	"testing"
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
