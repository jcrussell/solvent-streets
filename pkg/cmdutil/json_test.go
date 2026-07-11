package cmdutil

import (
	"errors"
	"strings"
	"testing"
	"text/template"

	"github.com/jcrussell/solvent-streets/pkg/iostreams"

	"github.com/itchyny/gojq"
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

// TestJQFilterExporter_Write guards the []map[string]any → []any
// widening (json.go:142-149). gojq's .[] iterator rejects the narrower
// slice type at runtime, so a regression here would surface only when
// users actually invoke --jq on multi-row output.
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

// TestTemplateExporter_Write confirms the simplified per-row loop
// handles the common []map input. The old scalar-map branch was
// removed; every caller now goes through WriteRows, so only the slice
// shape needs to work.
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
