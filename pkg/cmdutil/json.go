package cmdutil

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"text/template"

	"pvmt/pkg/iostreams"

	"github.com/itchyny/gojq"
	"github.com/spf13/cobra"
)

// Exporter writes structured data to an IOStreams output.
type Exporter interface {
	Fields() []string
	Write(ios *iostreams.IOStreams, data any) error
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

type jsonExporter struct {
	baseExporter
}

func (e *jsonExporter) Write(ios *iostreams.IOStreams, data any) error {
	filtered, err := filterFields(data, e.fields)
	if err != nil {
		return err
	}
	out, err := json.MarshalIndent(filtered, "", "  ")
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

func (e *jqFilterExporter) Write(ios *iostreams.IOStreams, data any) error {
	// filterFields returns generic map types already suitable for gojq
	filtered, err := filterFields(data, e.fields)
	if err != nil {
		return err
	}

	query, err := gojq.Parse(e.expr)
	if err != nil {
		return fmt.Errorf("invalid jq expression: %w", err)
	}

	iter := query.Run(filtered)
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

func (e *templateExporter) Write(ios *iostreams.IOStreams, data any) error {
	filtered, err := filterFields(data, e.fields)
	if err != nil {
		return err
	}

	tmpl, err := template.New("").Parse(e.tmpl)
	if err != nil {
		return fmt.Errorf("invalid template: %w", err)
	}

	// filterFields already returns []map[string]any or map[string]any
	switch v := filtered.(type) {
	case []map[string]any:
		for _, item := range v {
			if err := tmpl.Execute(ios.Out, item); err != nil {
				return fmt.Errorf("template error: %w", err)
			}
			fmt.Fprintln(ios.Out)
		}
	case map[string]any:
		if err := tmpl.Execute(ios.Out, v); err != nil {
			return fmt.Errorf("template error: %w", err)
		}
		fmt.Fprintln(ios.Out)
	default:
		return errors.New("template: unsupported data type")
	}
	return nil
}

// filterFields takes data (struct, slice, or map) and returns only the requested fields
// as generic map types ([]map[string]any or map[string]any).
func filterFields(data any, fields []string) (any, error) {
	// Marshal to JSON then unmarshal to generic structure
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	// Try as array first
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err == nil {
		result := make([]map[string]any, len(arr))
		for i, item := range arr {
			filtered := make(map[string]any)
			for _, f := range fields {
				if v, ok := item[f]; ok {
					filtered[f] = v
				}
			}
			result[i] = filtered
		}
		return result, nil
	}

	// Try as single object
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		filtered := make(map[string]any)
		for _, f := range fields {
			if v, ok := obj[f]; ok {
				filtered[f] = v
			}
		}
		return filtered, nil
	}

	// Pass through as-is
	return data, nil
}
