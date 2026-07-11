package iostreams

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// TablePrinter renders tabular data with TTY-aware formatting.
type TablePrinter struct {
	out     io.Writer
	isTTY   bool
	cs      *ColorScheme
	headers []string
	rows    [][]string
}

func NewTablePrinter(ios *IOStreams) *TablePrinter {
	return &TablePrinter{
		out:   ios.Out,
		isTTY: ios.IsTTY(),
		cs:    ios.ColorScheme(),
	}
}

func (t *TablePrinter) AddHeader(columns ...string) {
	t.headers = columns
}

func (t *TablePrinter) AddRow(columns ...string) {
	t.rows = append(t.rows, columns)
}

// Render outputs the table. TTY: padded columns with bold headers.
// Non-TTY: tab-separated, no headers.
func (t *TablePrinter) Render() error {
	if !t.isTTY {
		return t.renderPlain()
	}
	return t.renderTTY()
}

func (t *TablePrinter) renderPlain() error {
	for _, row := range t.rows {
		if _, err := fmt.Fprintln(t.out, strings.Join(row, "\t")); err != nil {
			return err
		}
	}
	return nil
}

func (t *TablePrinter) columnWidths() ([]int, int) {
	ncols := len(t.headers)
	if ncols == 0 && len(t.rows) > 0 {
		ncols = len(t.rows[0])
	}
	widths := make([]int, ncols)
	for i, h := range t.headers {
		if w := utf8.RuneCountInString(h); w > widths[i] {
			widths[i] = w
		}
	}
	for _, row := range t.rows {
		for i, col := range row {
			if i < ncols {
				if w := utf8.RuneCountInString(col); w > widths[i] {
					widths[i] = w
				}
			}
		}
	}
	return widths, ncols
}

// pad left-aligns s in a field of width display cells, measured in runes.
// The fmt "%-*s" verb counts bytes, so multi-byte runes (e.g. "José")
// would over-pad; appending spaces by rune delta keeps columns aligned.
func pad(s string, width int) string {
	gap := width - utf8.RuneCountInString(s)
	if gap <= 0 {
		return s
	}
	return s + strings.Repeat(" ", gap)
}

func (t *TablePrinter) renderTTY() error {
	widths, ncols := t.columnWidths()

	if len(t.headers) > 0 {
		parts := make([]string, len(t.headers))
		for i, h := range t.headers {
			parts[i] = t.cs.Bold(pad(h, widths[i]))
		}
		fmt.Fprintln(t.out, strings.Join(parts, "  "))
	}

	for _, row := range t.rows {
		parts := make([]string, len(row))
		for i, col := range row {
			if i < ncols {
				parts[i] = pad(col, widths[i])
			} else {
				parts[i] = col
			}
		}
		fmt.Fprintln(t.out, strings.Join(parts, "  "))
	}

	return nil
}
