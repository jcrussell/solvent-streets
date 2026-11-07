package cmdutil

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"text/template"

	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/itchyny/gojq"
	"github.com/spf13/cobra"
)

// fakeRow is a minimal RowExporter used by WriteRows tests.
type fakeRow struct {
	Name  string
	Count int
}

func (r fakeRow) ExportData(fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		switch f {
		case "name":
			out[f] = r.Name
		case "count":
			out[f] = r.Count
		}
	}
	return out
}

// TestJSONExporter_Write verifies the round-trip, and crucially that an
// empty rows slice marshals to "[]", not "null" — jq and template
// consumers downstream rely on the empty-array shape.
func TestJSONExporter_Write(t *testing.T) {
	t.Run("populated rows", func(t *testing.T) {
		ios, _, out, _ := iostreams.Test()
		e := &jsonExporter{baseExporter: baseExporter{fields: []string{"name", "count"}}}
		rows := []map[string]any{{"name": "a", "count": 1}, {"name": "b", "count": 2}}
		if err := e.Write(ios, rows); err != nil {
			t.Fatal(err)
		}
		got := out.String()
		if !strings.Contains(got, `"name": "a"`) || !strings.Contains(got, `"count": 2`) {
			t.Errorf("missing expected fields in output: %q", got)
		}
	})
	t.Run("empty rows marshals to [] not null", func(t *testing.T) {
		ios, _, out, _ := iostreams.Test()
		e := &jsonExporter{baseExporter: baseExporter{fields: []string{"name"}}}
		if err := e.Write(ios, []map[string]any{}); err != nil {
			t.Fatal(err)
		}
		got := strings.TrimSpace(out.String())
		if got != "[]" {
			t.Errorf("empty rows = %q, want %q", got, "[]")
		}
	})
}

// TestBuildExporter_TrimsSpacedFields verifies that a comma-separated
// field list with surrounding whitespace (e.g. "name, count") stores the
// trimmed names, so ExportData's exact-match switches emit every key.
func TestBuildExporter_TrimsSpacedFields(t *testing.T) {
	validFields := []string{"name", "count"}
	tests := []struct {
		name   string
		fields string
		want   []string
	}{
		{name: "no spaces", fields: "name,count", want: []string{"name", "count"}},
		{name: "spaced list", fields: "name, count", want: []string{"name", "count"}},
		{name: "padded list", fields: " name , count ", want: []string{"name", "count"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var e Exporter
			if err := buildExporter(tt.fields, "", "", validFields, &e); err != nil {
				t.Fatalf("buildExporter(%q) returned error: %v", tt.fields, err)
			}
			got := e.Fields()
			if len(got) != len(tt.want) {
				t.Fatalf("Fields() = %v, want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("Fields()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}

			// The stored names must round-trip through ExportData so spaced
			// lists produce both keys, not <no value>.
			row := fakeRow{Name: "x", Count: 7}.ExportData(got)
			for _, f := range tt.want {
				if _, ok := row[f]; !ok {
					t.Errorf("ExportData output missing key %q (got keys %v)", f, row)
				}
			}
		})
	}
}

// TestJQFilterExporter_Write guards that rows reach gojq as []any: its .[]
// iterator rejects []map[string]any at runtime, so a regression here would
// surface only when users actually invoke --jq on multi-row output.
func TestJQFilterExporter_Write(t *testing.T) {
	t.Run("selector over rows", func(t *testing.T) {
		ios, _, out, _ := iostreams.Test()
		query, err := gojq.Parse(".[].name")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		e := &jqFilterExporter{
			baseExporter: baseExporter{fields: []string{"name", "count"}},
			query:        query,
		}
		rows := []map[string]any{{"name": "first", "count": 1}, {"name": "second", "count": 2}}
		if err := e.Write(ios, rows); err != nil {
			t.Fatal(err)
		}
		got := out.String()
		if !strings.Contains(got, `"first"`) || !strings.Contains(got, `"second"`) {
			t.Errorf("jq output missing names: %q", got)
		}
	})
}

// TestTemplateExporter_Write confirms the template runs once per row.
func TestTemplateExporter_Write(t *testing.T) {
	t.Run("per-row template execution", func(t *testing.T) {
		ios, _, out, _ := iostreams.Test()
		tmpl, err := template.New("").Parse(`{{.name}}={{.count}}`)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		e := &templateExporter{
			baseExporter: baseExporter{fields: []string{"name", "count"}},
			tmpl:         tmpl,
		}
		rows := []map[string]any{{"name": "a", "count": 1}, {"name": "b", "count": 2}}
		if err := e.Write(ios, rows); err != nil {
			t.Fatal(err)
		}
		got := out.String()
		if !strings.Contains(got, "a=1") || !strings.Contains(got, "b=2") {
			t.Errorf("unexpected template output: %q", got)
		}
	})
}

// nestedRow and nestedKind stand in for the row values commands hand to the
// exporters: a slice of tagged structs and a named string type.
type nestedKind string

type nestedRow struct {
	RunID int64      `json:"run_id"`
	Kind  nestedKind `json:"kind"`
}

// TestExporters_SeeMarshaledRows guards that --jq and --template see the
// same keys and plain values --json prints: struct json tags rather than Go
// field names, named string types as strings, and numbers in their JSON
// spelling (ints stay ints; large floats are not rendered in e-notation).
func TestExporters_SeeMarshaledRows(t *testing.T) {
	rows := func() []map[string]any {
		return []map[string]any{{
			"parts": []nestedRow{{RunID: 12, Kind: "roads"}},
			"kind":  nestedKind("combined"),
			"area":  1500000.0,
		}}
	}
	fields := []string{"parts", "kind", "area"}

	t.Run("jq", func(t *testing.T) {
		var e Exporter
		if err := buildExporter("parts,kind,area", `[.[0].parts[0].run_id, .[0].parts[0].kind, .[0].kind, .[0].area, .[0].parts[0].run_id + 1]`, "", fields, &e); err != nil {
			t.Fatal(err)
		}
		ios, _, out, _ := iostreams.Test()
		if err := e.Write(ios, rows()); err != nil {
			t.Fatalf("Write: %v", err)
		}
		var got []any
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("jq output is not JSON: %v\n%s", err, out.String())
		}
		compact := strings.Join(strings.Fields(out.String()), "")
		if want := `[12,"roads","combined",1500000,13]`; compact != want {
			t.Errorf("jq output = %s, want %s", compact, want)
		}
	})
	t.Run("template", func(t *testing.T) {
		var e Exporter
		tmpl := `{{range .parts}}{{.run_id}} {{.kind}}{{end}} {{.kind}} {{.area}}`
		if err := buildExporter("parts,kind,area", "", tmpl, fields, &e); err != nil {
			t.Fatal(err)
		}
		ios, _, out, _ := iostreams.Test()
		if err := e.Write(ios, rows()); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if got, want := strings.TrimSpace(out.String()), "12 roads combined 1500000"; got != want {
			t.Errorf("template output = %q, want %q", got, want)
		}
	})
}

// TestAddJSONFlags_RecordsFieldsAnnotation guards the annotation the
// root-level field-name test walks.
func TestAddJSONFlags_RecordsFieldsAnnotation(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	var e Exporter
	AddJSONFlags(cmd, &e, []string{"city", "run_id"})
	if got := cmd.Annotations[JSONFieldsAnnotation]; got != "city,run_id" {
		t.Errorf("annotation = %q, want %q", got, "city,run_id")
	}
}

// TestBuildExporter_ParsesExpressionsEarly verifies that a malformed --jq
// or --template expression is rejected by buildExporter (run via PreRunE)
// as a FlagError — the exit-code-2 class — before the command body runs,
// and that valid expressions still produce a working exporter.
func TestBuildExporter_ParsesExpressionsEarly(t *testing.T) {
	validFields := []string{"name", "count"}

	assertFlagError := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("got nil error, want FlagError")
		}
		var fe *FlagError
		if !errors.As(err, &fe) {
			t.Fatalf("got error of type %T (%v), want *FlagError", err, err)
		}
	}

	t.Run("bad jq is a FlagError", func(t *testing.T) {
		var e Exporter
		err := buildExporter("name", ".foo |", "", validFields, &e)
		assertFlagError(t, err)
		if e != nil {
			t.Errorf("exporter should not be set on parse failure, got %T", e)
		}
	})
	t.Run("bad template is a FlagError", func(t *testing.T) {
		var e Exporter
		err := buildExporter("name", "", "{{.name", validFields, &e)
		assertFlagError(t, err)
		if e != nil {
			t.Errorf("exporter should not be set on parse failure, got %T", e)
		}
	})
	t.Run("valid jq builds a working exporter", func(t *testing.T) {
		var e Exporter
		if err := buildExporter("name", ".[].name", "", validFields, &e); err != nil {
			t.Fatalf("buildExporter: %v", err)
		}
		ios, _, out, _ := iostreams.Test()
		if err := e.Write(ios, []map[string]any{{"name": "x"}}); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if !strings.Contains(out.String(), `"x"`) {
			t.Errorf("jq output missing value: %q", out.String())
		}
	})
	t.Run("valid template builds a working exporter", func(t *testing.T) {
		var e Exporter
		if err := buildExporter("name", "", "{{.name}}", validFields, &e); err != nil {
			t.Fatalf("buildExporter: %v", err)
		}
		ios, _, out, _ := iostreams.Test()
		if err := e.Write(ios, []map[string]any{{"name": "x"}}); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if !strings.Contains(out.String(), "x") {
			t.Errorf("template output missing value: %q", out.String())
		}
	})
}

// TestWriteRows verifies the generic helper asks each row for exactly
// the Exporter's Fields() and forwards the shaped slice — the contract
// every subcommand relies on.
func TestWriteRows(t *testing.T) {
	ios, _, out, _ := iostreams.Test()
	e := &jsonExporter{baseExporter: baseExporter{fields: []string{"name"}}}
	rows := []fakeRow{{Name: "x", Count: 99}, {Name: "y", Count: 100}}
	if err := WriteRows(ios, e, rows); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, `"name": "x"`) || !strings.Contains(got, `"name": "y"`) {
		t.Errorf("expected both names in output: %q", got)
	}
	// count is not in Fields(), so ExportData must not emit it.
	if strings.Contains(got, "count") {
		t.Errorf("count was not requested but appeared in output: %q", got)
	}
}

// docCmd runs a command carrying AddDocumentJSONFlags with args and returns
// what --json / --jq / --template printed for doc.
func docCmd(t *testing.T, doc any, args ...string) (string, error) {
	t.Helper()
	ios, _, out, _ := iostreams.Test()
	var exp *DocumentExporter
	cmd := &cobra.Command{
		Use: "doc",
		RunE: func(*cobra.Command, []string) error {
			if exp == nil {
				_, err := out.WriteString("table\n")
				return err
			}
			return exp.Write(ios, doc)
		},
	}
	AddDocumentJSONFlags(cmd, &exp, "Emit the document as JSON")
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.Execute()
	return out.String(), err
}

type fakeDoc struct {
	FileCount int        `json:"file_count"`
	Cities    []fakeCity `json:"cities"`
}

type fakeCity struct {
	Slug  string `json:"slug"`
	Bytes int    `json:"bytes"`
}

var testDoc = fakeDoc{FileCount: 3, Cities: []fakeCity{{"a", 1500000}, {"b", 2}}}

// A one-document command's --json prints the document itself (not a row
// array), and --jq / --template see that whole document.
func TestDocumentExporter_FiltersTheWholeDocument(t *testing.T) {
	t.Run("--json prints the document", func(t *testing.T) {
		got, err := docCmd(t, testDoc, "--json")
		if err != nil {
			t.Fatal(err)
		}
		var back fakeDoc
		if err := json.Unmarshal([]byte(got), &back); err != nil {
			t.Fatalf("not one JSON document (%v):\n%s", err, got)
		}
		if back.FileCount != 3 || len(back.Cities) != 2 {
			t.Errorf("round-tripped %+v, want %+v", back, testDoc)
		}
	})
	t.Run("--jq runs over the document", func(t *testing.T) {
		got, err := docCmd(t, testDoc, "--json", "--jq", ".cities[] | .slug")
		if err != nil {
			t.Fatal(err)
		}
		if got != "\"a\"\n\"b\"\n" {
			t.Errorf("got %q, want each slug on its own line", got)
		}
	})
	t.Run("--template runs once over the document", func(t *testing.T) {
		got, err := docCmd(t, testDoc, "--json", "--template", "{{.file_count}}:{{range .cities}} {{.bytes}}{{end}}")
		if err != nil {
			t.Fatal(err)
		}
		// json.Number keeps 1500000 from rendering as 1.5e+06.
		if got != "3: 1500000 2\n" {
			t.Errorf("got %q, want %q", got, "3: 1500000 2\n")
		}
	})
	t.Run("no --json leaves the exporter nil", func(t *testing.T) {
		got, err := docCmd(t, testDoc)
		if err != nil {
			t.Fatal(err)
		}
		if got != "table\n" {
			t.Errorf("got %q, want the non-JSON path", got)
		}
	})
}

// Misuse fails as a FlagError (exit 2) before the command body runs.
func TestAddDocumentJSONFlags_RejectsMisuseEarly(t *testing.T) {
	for _, args := range [][]string{
		{"--jq", ".file_count"},
		{"--template", "{{.file_count}}"},
		{"--json", "--jq", ".["},
		{"--json", "--template", "{{"},
	} {
		got, err := docCmd(t, testDoc, args...)
		var fe *FlagError
		if !errors.As(err, &fe) {
			t.Errorf("%v: err = %v, want a FlagError", args, err)
		}
		if got != "" {
			t.Errorf("%v: command body ran and printed %q", args, got)
		}
	}
}
