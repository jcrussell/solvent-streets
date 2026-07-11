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
	for i := range fields {
		fields[i] = strings.TrimSpace(fields[i])
		if !slices.Contains(validFields, fields[i]) {
			return FlagErrorf("unknown JSON field %q; available: %s", fields[i], strings.Join(validFields, ", "))
		}
	}
	base := baseExporter{fields: fields}
	switch {
	case jqExpr != "":
		// Parse the jq expression at build time (PreRunE) so a syntax
		// error surfaces as a FlagError (exit 2) before the command body
		// runs, rather than after the full compute inside Write (exit 1).
		query, err := gojq.Parse(jqExpr)
		if err != nil {
			return FlagErrorf("invalid jq expression: %w", err)
		}
		*out = &jqFilterExporter{baseExporter: base, query: query}
	case tmplStr != "":
		// Parse the template at build time for the same reason; Write
		// reuses the precompiled *template.Template.
		tmpl, err := template.New("").Parse(tmplStr)
		if err != nil {
			return FlagErrorf("invalid template: %w", err)
		}
		*out = &templateExporter{baseExporter: base, tmpl: tmpl}
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

// jqFilterExporter applies a jq expression to the JSON output. The query
// is precompiled in buildExporter so parse errors surface early as
// FlagErrors rather than after the command body runs.
type jqFilterExporter struct {
	baseExporter
	query *gojq.Query
}

func (e *jqFilterExporter) Write(ios *iostreams.IOStreams, rows []map[string]any) error {
	// gojq evaluates .[] on []any, not []map[string]any — rebuild the
	// slice with the wider element type so jq expressions work as users
	// expect on multi-row output.
	generic := make([]any, len(rows))
	for i, r := range rows {
		generic[i] = r
	}

	iter := e.query.Run(generic)
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

// templateExporter applies a Go template to the JSON output. The template
// is precompiled in buildExporter so parse errors surface early as
// FlagErrors rather than after the command body runs.
type templateExporter struct {
	baseExporter
	tmpl *template.Template
}

func (e *templateExporter) Write(ios *iostreams.IOStreams, rows []map[string]any) error {
	for _, item := range rows {
		if err := e.tmpl.Execute(ios.Out, item); err != nil {
			return fmt.Errorf("template error: %w", err)
		}
		fmt.Fprintln(ios.Out)
	}
	return nil
}
