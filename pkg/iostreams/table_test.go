package iostreams

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestTablePrinter_PlainOutput pins the non-TTY render path: rows are
// tab-separated, headers are dropped, and no ANSI escapes appear. This is
// the contract that lets a caller pipe `pvmt … | cut -f1` without the
// padding/colors a human terminal needs.
func TestTablePrinter_PlainOutput(t *testing.T) {
	ios, _, out, _ := Test()
	tp := NewTablePrinter(ios)
	tp.AddHeader("NAME", "AREA")
	tp.AddRow("Boston", "232.1")
	tp.AddRow("NYC", "1213.4")

	if err := tp.Render(); err != nil {
		t.Fatalf("Render: %v", err)
	}

	want := "Boston\t232.1\nNYC\t1213.4\n"
	if got := out.String(); got != want {
		t.Errorf("plain output mismatch\n got: %q\nwant: %q", got, want)
	}
	if strings.Contains(out.String(), "\033[") {
		t.Errorf("plain output leaked ANSI escapes: %q", out.String())
	}
}

// TestTablePrinter_TTYAlignment pins the TTY render path: each column is
// padded to the width of its widest cell (header included), columns are
// separated by exactly two spaces, headers are emitted, and the header row
// is bold when color is enabled. The padding rule is what callers depend on
// when eyeballing a table — a width regression turns the output into ragged
// noise.
func TestTablePrinter_TTYAlignment(t *testing.T) {
	ios, _, out, _ := Test()
	ios.SetTTY(true)
	ios.isColorEnabled = true
	ios.colorScheme = NewColorScheme(true)

	tp := NewTablePrinter(ios)
	tp.AddHeader("CITY", "PCT")
	tp.AddRow("Boston", "42")
	tp.AddRow("LA", "7")

	if err := tp.Render(); err != nil {
		t.Fatalf("Render: %v", err)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines (header + 2 rows); got %d: %q", len(lines), lines)
	}

	// Widest cell in col0 is "Boston" (6); col1 is "PCT" (3). Body lines
	// should be "<col0 padded to 6>  <col1 padded to 3>".
	wantBody := []string{
		"Boston  42 ",
		"LA      7  ",
	}
	for i, want := range wantBody {
		if lines[i+1] != want {
			t.Errorf("row %d: got %q, want %q", i, lines[i+1], want)
		}
	}

	// Header line wraps each padded header in bold; stripping ANSI must
	// leave the same padded layout as the body.
	header := lines[0]
	if !strings.Contains(header, "\033[1m") {
		t.Errorf("header missing bold ANSI; got %q", header)
	}
	stripped := strings.ReplaceAll(strings.ReplaceAll(header, "\033[1m", ""), "\033[0m", "")
	if stripped != "CITY    PCT" {
		t.Errorf("header padded layout mismatch: got %q, want %q", stripped, "CITY    PCT")
	}
}

// TestTablePrinter_NoHeaderInfersWidthFromFirstRow exercises the
// columnWidths branch where headers are empty: ncols falls back to
// len(rows[0]) and padding still works.
func TestTablePrinter_NoHeaderInfersWidthFromFirstRow(t *testing.T) {
	ios, _, out, _ := Test()
	ios.SetTTY(true)

	tp := NewTablePrinter(ios)
	tp.AddRow("a", "bb")
	tp.AddRow("ccc", "d")

	if err := tp.Render(); err != nil {
		t.Fatalf("Render: %v", err)
	}

	want := "a    bb\nccc  d \n"
	if got := out.String(); got != want {
		t.Errorf("no-header output mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestRelativeTime walks each bucket of the duration switch. We feed a
// fixed time.Time anchored to time.Now() rather than freezing a clock —
// the helper reads time.Since directly, so tests must accept the staleness
// the bucket boundaries permit.
func TestRelativeTime(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{"just now", now.Add(-10 * time.Second), "just now"},
		{"minutes", now.Add(-5 * time.Minute), "5 minutes ago"},
		{"hours", now.Add(-3 * time.Hour), "3 hours ago"},
		{"days", now.Add(-48 * time.Hour), "2 days ago"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RelativeTime(tc.at); got != tc.want {
				t.Errorf("RelativeTime(%v) = %q, want %q", tc.at, got, tc.want)
			}
		})
	}
}

// TestFormatTimestamp pins the three branches: empty → "never", non-TTY
// echoes raw input, TTY appends a relative-time suffix.
func TestFormatTimestamp(t *testing.T) {
	if got := FormatTimestamp("", false); got != "never" {
		t.Errorf("empty/non-TTY: got %q, want \"never\"", got)
	}
	if got := FormatTimestamp("", true); got != "never" {
		t.Errorf("empty/TTY: got %q, want \"never\"", got)
	}

	raw := "2026-05-01T12:00:00Z"
	if got := FormatTimestamp(raw, false); got != raw {
		t.Errorf("non-TTY echo: got %q, want %q", got, raw)
	}

	recent := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	got := FormatTimestamp(recent, true)
	wantPrefix := recent + " ("
	if !strings.HasPrefix(got, wantPrefix) || !strings.HasSuffix(got, ")") {
		t.Errorf("TTY suffix shape: got %q, want %q…)", got, wantPrefix)
	}
	if !strings.Contains(got, "hours ago") {
		t.Errorf("TTY suffix content: got %q, want a 'hours ago' fragment", got)
	}
}

// TestFormatTimestamp_TTYUnparseableFallsBackToZeroTime documents the
// current behavior on malformed RFC3339 input under a TTY: time.Parse
// returns the zero value, which RelativeTime renders as "N days ago"
// for an extreme N. The helper does not surface the parse error. This
// test pins the fallback shape so a future change that starts returning
// an error or swallowing the suffix becomes visible.
func TestFormatTimestamp_TTYUnparseableFallsBackToZeroTime(t *testing.T) {
	got := FormatTimestamp("not-a-timestamp", true)
	if !strings.HasPrefix(got, "not-a-timestamp (") {
		t.Errorf("malformed input prefix: got %q, want %q", got, "not-a-timestamp (…)")
	}
	if !strings.HasSuffix(got, "days ago)") {
		t.Errorf("malformed input suffix: got %q, want suffix %q", got, "days ago)")
	}
}

// TestTablePrinter_RowWiderThanHeader exercises the rendering branch
// where a row has more columns than the header: padded cells use the
// computed widths and overflow cells are appended unpadded.
func TestTablePrinter_RowWiderThanHeader(t *testing.T) {
	ios, _, out, _ := Test()
	ios.SetTTY(true)

	tp := NewTablePrinter(ios)
	tp.AddHeader("A", "B")
	tp.AddRow("xx", "yy", "extra")

	if err := tp.Render(); err != nil {
		t.Fatalf("Render: %v", err)
	}

	// header (no color) "A   B " then row "xx  yy  extra"
	want := fmt.Sprintf("A   B \nxx  yy  %s\n", "extra")
	if got := out.String(); got != want {
		t.Errorf("overflow row mismatch\n got: %q\nwant: %q", got, want)
	}
}
