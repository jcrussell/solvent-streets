package cmdutil

import (
	"errors"
	"log/slog"
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

func TestLogLevel_Set(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantLevel slog.Level
		wantErr   bool
	}{
		{"debug", "debug", slog.LevelDebug, false},
		{"info", "info", slog.LevelInfo, false},
		{"warn", "warn", slog.LevelWarn, false},
		{"warning alias", "warning", slog.LevelWarn, false},
		{"error", "error", slog.LevelError, false},
		{"case insensitive", "DEBUG", slog.LevelDebug, false},
		{"empty rejected", "", 0, true},
		{"unknown rejected", "fatal", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var l LogLevel
			err := l.Set(tt.in)
			if tt.wantErr {
				var flagErr *FlagError
				if !errors.As(err, &flagErr) {
					t.Fatalf("expected FlagError, got %T: %v", err, err)
				}
				if l.IsSet() {
					t.Errorf("IsSet=true after rejected Set")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !l.IsSet() {
				t.Errorf("IsSet=false after successful Set")
			}
			if l.Level() != tt.wantLevel {
				t.Errorf("Level()=%v, want %v", l.Level(), tt.wantLevel)
			}
		})
	}
}

// TestLogLevel_ZeroValue asserts an unset LogLevel reports IsSet=false so
// applyLogLevel falls through to -v / PVMT_LOG / default rather than
// pinning the level to a zero slog.Level (Info).
func TestLogLevel_ZeroValue(t *testing.T) {
	var l LogLevel
	if l.IsSet() {
		t.Error("zero-value LogLevel reports IsSet=true")
	}
	if (*LogLevel)(nil).IsSet() {
		t.Error("nil *LogLevel reports IsSet=true")
	}
}

func TestParseLogLevel(t *testing.T) {
	cases := []struct {
		in     string
		level  slog.Level
		wantOK bool
	}{
		{"debug", slog.LevelDebug, true},
		{"info", slog.LevelInfo, true},
		{"warn", slog.LevelWarn, true},
		{"warning", slog.LevelWarn, true},
		{"error", slog.LevelError, true},
		{"ERROR", slog.LevelError, true},
		{"", slog.LevelWarn, false},
		{"trace", slog.LevelWarn, false},
	}
	for _, c := range cases {
		got, ok := ParseLogLevel(c.in)
		if ok != c.wantOK {
			t.Errorf("ParseLogLevel(%q) ok=%v, want %v", c.in, ok, c.wantOK)
		}
		if got != c.level {
			t.Errorf("ParseLogLevel(%q) level=%v, want %v", c.in, got, c.level)
		}
	}
}
