package iostreams

import (
	"fmt"
	"io"
	"strings"
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
		if len(h) > widths[i] {
			widths[i] = len(h)
		}
	}
	for _, row := range t.rows {
		for i, col := range row {
			if i < ncols && len(col) > widths[i] {
				widths[i] = len(col)
			}
		}
	}
	return widths, ncols
}

func (t *TablePrinter) renderTTY() error {
	widths, ncols := t.columnWidths()

	if len(t.headers) > 0 {
		parts := make([]string, len(t.headers))
		for i, h := range t.headers {
			padded := fmt.Sprintf("%-*s", widths[i], h)
			parts[i] = t.cs.Bold(padded)
		}
		fmt.Fprintln(t.out, strings.Join(parts, "  "))
	}

	for _, row := range t.rows {
		parts := make([]string, len(row))
		for i, col := range row {
			if i < ncols {
				parts[i] = fmt.Sprintf("%-*s", widths[i], col)
			} else {
				parts[i] = col
			}
		}
		fmt.Fprintln(t.out, strings.Join(parts, "  "))
	}

	return nil
}
