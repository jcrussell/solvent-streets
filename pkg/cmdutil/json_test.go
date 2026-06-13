package cmdutil

import (
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/pkg/iostreams"
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
		e := &jqFilterExporter{
			baseExporter: baseExporter{fields: []string{"name", "count"}},
			expr:         ".[].name",
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
	t.Run("invalid expression surfaces parse error", func(t *testing.T) {
		ios, _, _, _ := iostreams.Test()
		e := &jqFilterExporter{
			baseExporter: baseExporter{fields: []string{"name"}},
			expr:         "[[[",
		}
		if err := e.Write(ios, []map[string]any{{"name": "x"}}); err == nil {
			t.Error("expected parse error, got nil")
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
		e := &templateExporter{
			baseExporter: baseExporter{fields: []string{"name", "count"}},
			tmpl:         `{{.name}}={{.count}}`,
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
	t.Run("invalid template surfaces parse error", func(t *testing.T) {
		ios, _, _, _ := iostreams.Test()
		e := &templateExporter{
			baseExporter: baseExporter{fields: []string{"name"}},
			tmpl:         "{{.name",
		}
		if err := e.Write(ios, []map[string]any{{"name": "x"}}); err == nil {
			t.Error("expected template parse error, got nil")
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
