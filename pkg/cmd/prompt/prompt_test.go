package prompt_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jcrussell/solvent-streets/pkg/cmd/prompt"
	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

func TestStubConfirmPopsFIFO(t *testing.T) {
	s := &prompt.Stub{Confirms: []bool{true, false}}
	ctx := context.Background()

	got, err := s.Confirm(ctx, "first?", false)
	if err != nil {
		t.Fatalf("Confirm[0]: %v", err)
	}
	if !got {
		t.Errorf("Confirm[0] = false, want true")
	}

	got, err = s.Confirm(ctx, "second?", true)
	if err != nil {
		t.Fatalf("Confirm[1]: %v", err)
	}
	if got {
		t.Errorf("Confirm[1] = true, want false")
	}
}

func TestStubConfirmPanicsWhenEmpty(t *testing.T) {
	s := &prompt.Stub{}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when Confirm called with empty FIFO")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("expected string panic, got %T", r)
		}
		if !strings.Contains(msg, "thirsty?") {
			t.Errorf("panic message %q should include the prompt", msg)
		}
	}()
	_, _ = s.Confirm(context.Background(), "thirsty?", false)
}

func TestStubAllMethodsPopIndependently(t *testing.T) {
	s := &prompt.Stub{
		Confirms:     []bool{true},
		Inputs:       []string{"foo"},
		Passwords:    []string{"hunter2"},
		Selects:      []int{2},
		MultiSelects: [][]int{{0, 3}},
	}
	ctx := context.Background()

	if v, _ := s.Confirm(ctx, "", false); !v {
		t.Error("Confirm: want true")
	}
	if v, _ := s.Input(ctx, "", ""); v != "foo" {
		t.Errorf("Input: got %q, want foo", v)
	}
	if v, _ := s.Password(ctx, ""); v != "hunter2" {
		t.Errorf("Password: got %q, want hunter2", v)
	}
	if v, _ := s.Select(ctx, "", nil); v != 2 {
		t.Errorf("Select: got %d, want 2", v)
	}
	if v, _ := s.MultiSelect(ctx, "", nil); len(v) != 2 || v[0] != 0 || v[1] != 3 {
		t.Errorf("MultiSelect: got %v, want [0 3]", v)
	}
}

// ttyIO returns an IOStreams whose In holds the given canned input and
// whose stdin TTY flag is on, so Live methods don't short-circuit with
// ErrNotTTY.
func ttyIO(input string) *iostreams.IOStreams {
	io, in, _, _ := iostreams.Test()
	in.WriteString(input)
	io.SetStdinTTY(true)
	return io
}

func TestLiveReturnsErrNotTTYWhenStdinNotTTY(t *testing.T) {
	io, _, _, _ := iostreams.Test()
	p := prompt.NewLive(io)
	ctx := context.Background()

	cases := []struct {
		name string
		call func() error
	}{
		{"Confirm", func() error { _, err := p.Confirm(ctx, "?", false); return err }},
		{"Input", func() error { _, err := p.Input(ctx, "?", ""); return err }},
		{"Password", func() error { _, err := p.Password(ctx, "?"); return err }},
		{"Select", func() error { _, err := p.Select(ctx, "?", []string{"a"}); return err }},
		{"MultiSelect", func() error { _, err := p.MultiSelect(ctx, "?", []string{"a"}); return err }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.call()
			if !errors.Is(err, prompt.ErrNotTTY) {
				t.Errorf("got %v, want ErrNotTTY", err)
			}
		})
	}
}

func TestLiveConfirmAcceptsYesAndDefault(t *testing.T) {
	cases := []struct {
		name  string
		input string
		def   bool
		want  bool
	}{
		{"explicit-yes", "y\n", false, true},
		{"long-yes", "yes\n", false, true},
		{"explicit-no", "n\n", true, false},
		{"long-no", "no\n", true, false},
		{"empty-uses-default-true", "\n", true, true},
		{"empty-uses-default-false", "\n", false, false},
		{"case-insensitive", "YES\n", false, true},
		{"whitespace-trimmed", "  y  \n", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := prompt.NewLive(ttyIO(c.input))
			got, err := p.Confirm(context.Background(), "?", c.def)
			if err != nil {
				t.Fatalf("Confirm: %v", err)
			}
			if got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestLiveConfirmRejectsGarbage(t *testing.T) {
	p := prompt.NewLive(ttyIO("maybe?\n"))
	_, err := p.Confirm(context.Background(), "?", false)
	if err == nil || !strings.Contains(err.Error(), "invalid response") {
		t.Errorf("got %v, want invalid-response error", err)
	}
}

func TestLiveConfirmPropagatesEOF(t *testing.T) {
	// In is empty; Scan returns false with nil Err, which we surface as io.EOF.
	p := prompt.NewLive(ttyIO(""))
	_, err := p.Confirm(context.Background(), "?", false)
	if !errors.Is(err, io.EOF) {
		t.Errorf("got %v, want io.EOF", err)
	}
}

func TestLiveConfirmHonorsCtxCancel(t *testing.T) {
	io, _, _, _ := iostreams.Test()
	io.SetStdinTTY(true)
	io.In = hangReader{}
	p := prompt.NewLive(io)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Confirm(ctx, "?", false)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}

type hangReader struct{}

func (hangReader) Read(_ []byte) (int, error) { select {} }
func (hangReader) Close() error               { return nil }

func TestLiveInputUsesDefaultOnEmpty(t *testing.T) {
	p := prompt.NewLive(ttyIO("\n"))
	got, err := p.Input(context.Background(), "?", "fallback")
	if err != nil {
		t.Fatalf("Input: %v", err)
	}
	if got != "fallback" {
		t.Errorf("got %q, want fallback", got)
	}
}

func TestLiveInputReturnsTypedValue(t *testing.T) {
	p := prompt.NewLive(ttyIO("typed value\n"))
	got, err := p.Input(context.Background(), "?", "fallback")
	if err != nil {
		t.Fatalf("Input: %v", err)
	}
	if got != "typed value" {
		t.Errorf("got %q, want typed value", got)
	}
}

func TestLiveSelectParsesIndex(t *testing.T) {
	p := prompt.NewLive(ttyIO("2\n"))
	got, err := p.Select(context.Background(), "Pick", []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got != 1 {
		t.Errorf("got %d, want 1 (zero-indexed for option 2)", got)
	}
}

func TestLiveSelectRejectsOutOfRange(t *testing.T) {
	p := prompt.NewLive(ttyIO("99\n"))
	_, err := p.Select(context.Background(), "Pick", []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "invalid selection") {
		t.Errorf("got %v, want invalid-selection error", err)
	}
}

func TestLiveSelectRejectsEmptyOptions(t *testing.T) {
	io, _, _, _ := iostreams.Test()
	io.SetStdinTTY(true)
	p := prompt.NewLive(io)
	_, err := p.Select(context.Background(), "Pick", nil)
	if err == nil || !strings.Contains(err.Error(), "at least one option") {
		t.Errorf("got %v, want options-required error", err)
	}
}

func TestLiveMultiSelectParsesList(t *testing.T) {
	p := prompt.NewLive(ttyIO("1, 3\n"))
	got, err := p.MultiSelect(context.Background(), "Pick many", []string{"a", "b", "c", "d"})
	if err != nil {
		t.Fatalf("MultiSelect: %v", err)
	}
	if len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Errorf("got %v, want [0 2]", got)
	}
}

func TestLiveMultiSelectEmptyReturnsNil(t *testing.T) {
	p := prompt.NewLive(ttyIO("\n"))
	got, err := p.MultiSelect(context.Background(), "Pick", []string{"a", "b"})
	if err != nil {
		t.Fatalf("MultiSelect: %v", err)
	}
	if got != nil {
		t.Errorf("got %v, want nil for blank input", got)
	}
}

func TestLiveMultiSelectDeduplicates(t *testing.T) {
	p := prompt.NewLive(ttyIO("1,1,2\n"))
	got, err := p.MultiSelect(context.Background(), "Pick", []string{"a", "b"})
	if err != nil {
		t.Fatalf("MultiSelect: %v", err)
	}
	if len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Errorf("got %v, want [0 1] (duplicates removed)", got)
	}
}

func TestLiveMultiSelectRejectsOutOfRange(t *testing.T) {
	p := prompt.NewLive(ttyIO("1,99\n"))
	_, err := p.MultiSelect(context.Background(), "Pick", []string{"a"})
	if err == nil || !strings.Contains(err.Error(), "invalid selection") {
		t.Errorf("got %v, want invalid-selection error", err)
	}
}

func TestLivePasswordRejectsNonFdStdin(t *testing.T) {
	// IsStdinTTY=true but In is an in-memory buffer (no Fd method): the
	// guard in Password must reject this rather than crashing.
	io, _, _, _ := iostreams.Test()
	io.SetStdinTTY(true)
	p := prompt.NewLive(io)
	_, err := p.Password(context.Background(), "?")
	if err == nil || !strings.Contains(err.Error(), "Fd") {
		t.Errorf("got %v, want Fd-related error", err)
	}
}
