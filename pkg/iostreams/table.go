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
		for _, row := range t.rows {
			_, err := fmt.Fprintln(t.out, strings.Join(row, "\t"))
			if err != nil {
				return err
			}
		}
		return nil
	}

	// Calculate column widths from headers and all rows
	allRows := t.rows
	ncols := len(t.headers)
	if ncols == 0 && len(allRows) > 0 {
		ncols = len(allRows[0])
	}

	widths := make([]int, ncols)
	for i, h := range t.headers {
		if len(h) > widths[i] {
			widths[i] = len(h)
		}
	}
	for _, row := range allRows {
		for i, col := range row {
			if i < ncols && len(col) > widths[i] {
				widths[i] = len(col)
			}
		}
	}

	// Print headers
	if len(t.headers) > 0 {
		parts := make([]string, len(t.headers))
		for i, h := range t.headers {
			padded := fmt.Sprintf("%-*s", widths[i], h)
			parts[i] = t.cs.Bold(padded)
		}
		fmt.Fprintln(t.out, strings.Join(parts, "  "))
	}

	// Print rows
	for _, row := range allRows {
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
