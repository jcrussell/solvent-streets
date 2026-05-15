package cmdutil

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"text/template"

	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/itchyny/gojq"
	"github.com/spf13/cobra"
)

// Exporter writes pre-shaped rows to an IOStreams output. The rows
// argument must already be filtered to the requested JSON fields —
// callers should reach Exporter through WriteRows, which delegates the
// per-row filtering to RowExporter.ExportData. Taking []map[string]any
// rather than any keeps that contract visible at compile time instead
// of deferring it to runtime checks inside each implementation.
type Exporter interface {
	Fields() []string
	Write(ios *iostreams.IOStreams, rows []map[string]any) error
}

// RowExporter emits a filtered map[string]any for the requested JSON
// fields. Each resource row type implements this to define its JSON
// contract explicitly, rather than relying on json.Marshal reflection.
type RowExporter interface {
	ExportData(fields []string) map[string]any
}

// WriteRows shapes rows into the []map[string]any form Exporter expects
// and delegates to the exporter's Write. Factored out so the three
// subcommands that call --json don't each repeat the same loop.
func WriteRows[T RowExporter](ios *iostreams.IOStreams, e Exporter, rows []T) error {
	out := make([]map[string]any, len(rows))
	for i, r := range rows {
		out[i] = r.ExportData(e.Fields())
	}
	return e.Write(ios, out)
}

// AddJSONFlags adds --json, --jq, and --template flags to the command.
// When --json is set, creates an exporter stored in *exporter.
// --jq and --template post-process the JSON output and require --json.
func AddJSONFlags(cmd *cobra.Command, exporter *Exporter, validFields []string) {
	var jsonFields string
	var jqExpr string
	var tmplStr string

	cmd.Flags().StringVar(&jsonFields, "json", "", fmt.Sprintf("Output JSON with specified fields (available: %s)", strings.Join(validFields, ",")))
	cmd.Flags().StringVar(&jqExpr, "jq", "", "Filter JSON output using a jq expression (requires --json)")
	cmd.Flags().StringVar(&tmplStr, "template", "", "Format JSON output using a Go template (requires --json)")
	cmd.MarkFlagsMutuallyExclusive("jq", "template")

	oldPreRun := cmd.PreRunE
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		if oldPreRun != nil {
			if err := oldPreRun(cmd, args); err != nil {
				return err
			}
		}
		return buildExporter(jsonFields, jqExpr, tmplStr, validFields, exporter)
	}
}

func buildExporter(jsonFields, jqExpr, tmplStr string, validFields []string, out *Exporter) error {
	if jqExpr != "" && jsonFields == "" {
		return FlagErrorf("--jq requires --json")
	}
	if tmplStr != "" && jsonFields == "" {
		return FlagErrorf("--template requires --json")
	}
	if jsonFields == "" {
		return nil
	}

	fields := strings.Split(jsonFields, ",")
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if !slices.Contains(validFields, f) {
			return FlagErrorf("unknown JSON field %q; available: %s", f, strings.Join(validFields, ", "))
		}
	}
	base := baseExporter{fields: fields}
	switch {
	case jqExpr != "":
		*out = &jqFilterExporter{baseExporter: base, expr: jqExpr}
	case tmplStr != "":
		*out = &templateExporter{baseExporter: base, tmpl: tmplStr}
	default:
		*out = &jsonExporter{baseExporter: base}
	}
	return nil
}

// baseExporter holds the shared fields slice for all exporter types.
type baseExporter struct {
	fields []string
}

func (e *baseExporter) Fields() []string {
	return e.fields
}

var (
	_ Exporter = (*jsonExporter)(nil)
	_ Exporter = (*jqFilterExporter)(nil)
	_ Exporter = (*templateExporter)(nil)
)

type jsonExporter struct {
	baseExporter
}

func (e *jsonExporter) Write(ios *iostreams.IOStreams, rows []map[string]any) error {
	out, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}
	_, err = fmt.Fprintln(ios.Out, string(out))
	return err
}

// jqFilterExporter applies a jq expression to the JSON output.
type jqFilterExporter struct {
	baseExporter
	expr string
}

func (e *jqFilterExporter) Write(ios *iostreams.IOStreams, rows []map[string]any) error {
	query, err := gojq.Parse(e.expr)
	if err != nil {
		return fmt.Errorf("invalid jq expression: %w", err)
	}

	// gojq evaluates .[] on []any, not []map[string]any — rebuild the
	// slice with the wider element type so jq expressions work as users
	// expect on multi-row output.
	generic := make([]any, len(rows))
	for i, r := range rows {
		generic[i] = r
	}

	iter := query.Run(generic)
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, isErr := v.(error); isErr {
			return fmt.Errorf("jq error: %w", err)
		}
		out, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(ios.Out, string(out))
	}
	return nil
}

// templateExporter applies a Go template to the JSON output.
type templateExporter struct {
	baseExporter
	tmpl string
}

func (e *templateExporter) Write(ios *iostreams.IOStreams, rows []map[string]any) error {
	tmpl, err := template.New("").Parse(e.tmpl)
	if err != nil {
		return fmt.Errorf("invalid template: %w", err)
	}
	for _, item := range rows {
		if err := tmpl.Execute(ios.Out, item); err != nil {
			return fmt.Errorf("template error: %w", err)
		}
		fmt.Fprintln(ios.Out)
	}
	return nil
}
