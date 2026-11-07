package cmdutil

import (
	"bytes"
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

// JSONFieldsAnnotation is the cobra annotation key under which AddJSONFlags
// records a command's comma-joined --json field list.
const JSONFieldsAnnotation = "json_fields"

// AddJSONFlags adds --json, --jq, and --template flags to the command.
// When --json is set, creates an exporter stored in *exporter.
// --jq and --template post-process the JSON output and require --json.
// The valid fields are recorded in cmd.Annotations[JSONFieldsAnnotation].
func AddJSONFlags(cmd *cobra.Command, exporter *Exporter, validFields []string) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[JSONFieldsAnnotation] = strings.Join(validFields, ",")

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
	query, tmpl, err := parseFilters(jqExpr, tmplStr)
	if err != nil {
		return err
	}
	switch {
	case query != nil:
		*out = &jqFilterExporter{baseExporter: base, query: query}
	case tmpl != nil:
		*out = &templateExporter{baseExporter: base, tmpl: tmpl}
	default:
		*out = &jsonExporter{baseExporter: base}
	}
	return nil
}

// parseFilters compiles the --jq expression or --template text, whichever is
// set. It runs at build time (PreRunE) so a syntax error surfaces as a
// FlagError (exit 2) before the command body runs, rather than after the
// full compute inside Write (exit 1).
func parseFilters(jqExpr, tmplStr string) (*gojq.Query, *template.Template, error) {
	if jqExpr != "" {
		query, err := gojq.Parse(jqExpr)
		if err != nil {
			return nil, nil, FlagErrorf("invalid jq expression: %w", err)
		}
		return query, nil, nil
	}
	if tmplStr != "" {
		tmpl, err := template.New("").Parse(tmplStr)
		if err != nil {
			return nil, nil, FlagErrorf("invalid template: %w", err)
		}
		return nil, tmpl, nil
	}
	return nil, nil, nil
}

// DocumentExporter writes a command's single JSON document (a manifest or a
// report, not a list of rows), applying --jq or --template to the whole
// document. Commands get one from AddDocumentJSONFlags; it is nil when
// --json was not given.
type DocumentExporter struct {
	query *gojq.Query
	tmpl  *template.Template
}

// AddDocumentJSONFlags adds a boolean --json (described by usage) plus --jq
// and --template to a command whose JSON output is one document rather than
// rows, so it takes no field list. When --json is set, PreRunE stores a
// DocumentExporter in *exporter; --jq and --template require --json.
func AddDocumentJSONFlags(cmd *cobra.Command, exporter **DocumentExporter, usage string) {
	var asJSON bool
	var jqExpr string
	var tmplStr string

	cmd.Flags().BoolVar(&asJSON, "json", false, usage)
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
		if jqExpr != "" && !asJSON {
			return FlagErrorf("--jq requires --json")
		}
		if tmplStr != "" && !asJSON {
			return FlagErrorf("--template requires --json")
		}
		if !asJSON {
			*exporter = nil
			return nil
		}
		query, tmpl, err := parseFilters(jqExpr, tmplStr)
		if err != nil {
			return err
		}
		*exporter = &DocumentExporter{query: query, tmpl: tmpl}
		return nil
	}
}

// Write prints doc as indented JSON, or each jq result, or the template
// executed once over the whole document. --jq and --template see exactly
// what --json prints (see normalizeRows).
func (e *DocumentExporter) Write(ios *iostreams.IOStreams, doc any) error {
	if e.query == nil && e.tmpl == nil {
		out, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal JSON: %w", err)
		}
		_, err = fmt.Fprintln(ios.Out, string(out))
		return err
	}
	generic, err := normalizeJSON[any](doc)
	if err != nil {
		return err
	}
	if e.tmpl != nil {
		if err := e.tmpl.Execute(ios.Out, generic); err != nil {
			return fmt.Errorf("template error: %w", err)
		}
		_, err := fmt.Fprintln(ios.Out)
		return err
	}
	return writeJQ(ios, e.query, generic)
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
	generic, err := normalizeRows(rows)
	if err != nil {
		return err
	}
	return writeJQ(ios, e.query, generic)
}

// writeJQ runs query over input and prints each result as indented JSON.
func writeJQ(ios *iostreams.IOStreams, query *gojq.Query, input any) error {
	iter := query.Run(input)
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
	generic, err := normalizeRows(rows)
	if err != nil {
		return err
	}
	for _, item := range generic {
		if err := e.tmpl.Execute(ios.Out, item); err != nil {
			return fmt.Errorf("template error: %w", err)
		}
		fmt.Fprintln(ios.Out)
	}
	return nil
}

// normalizeRows round-trips rows through encoding/json so --jq and
// --template see exactly what --json prints: nested structs become maps
// keyed by their json tags, named string types become plain strings, and
// numbers become json.Number (which gojq accepts and templates print with
// their JSON spelling, so 1500000 does not render as 1.5e+06). The result
// is a []any of map[string]any, the shapes gojq evaluates.
func normalizeRows(rows []map[string]any) ([]any, error) {
	return normalizeJSON[[]any](rows)
}

// normalizeJSON round-trips v through encoding/json into T (see
// normalizeRows for why).
func normalizeJSON[T any](v any) (T, error) {
	var out T
	raw, err := json.Marshal(v)
	if err != nil {
		return out, fmt.Errorf("marshal JSON: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return out, fmt.Errorf("normalize JSON: %w", err)
	}
	return out, nil
}
