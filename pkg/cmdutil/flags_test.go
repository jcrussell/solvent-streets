package cmdutil

import (
	"errors"
	"testing"
)

func TestUnitSystem_Set(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    UnitSystem
		wantErr bool
	}{
		{"empty falls back", "", "", false},
		{"metric", "metric", UnitMetric, false},
		{"imperial", "imperial", UnitImperial, false},
		{"invalid", "bogus", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var u UnitSystem
			err := u.Set(tt.in)
			if tt.wantErr {
				var flagErr *FlagError
				if !errors.As(err, &flagErr) {
					t.Fatalf("expected FlagError, got %T: %v", err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if u != tt.want {
				t.Errorf("got %q, want %q", u, tt.want)
			}
		})
	}
}

func TestSource_Set(t *testing.T) {
	var s Source
	if err := s.Set("all"); err != nil {
		t.Fatalf("Set(all): %v", err)
	}
	if s != SourceAll {
		t.Errorf("got %q, want %q", s, SourceAll)
	}

	err := s.Set("bogus")
	var flagErr *FlagError
	if !errors.As(err, &flagErr) {
		t.Fatalf("expected FlagError, got %T: %v", err, err)
	}
}
