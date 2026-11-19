package services

import (
	"strings"
	"testing"
)

// A service with no SourceFormat publishes no methodology line rather than a
// Sprintf of an empty format (peer review of solvent-streets-znqw).
func TestSpecSourceText_NoFormatIsEmpty(t *testing.T) {
	if got := (Spec{Name: "made-up", TurnoutS: 60}).SourceText(); got != "" {
		t.Errorf("SourceText() = %q, want empty", got)
	}
	for name, s := range Registry {
		if s.SourceFormat != "" && strings.Contains(s.SourceText(), "%!") {
			t.Errorf("%s: SourceText() = %q has a format error", name, s.SourceText())
		}
	}
}
