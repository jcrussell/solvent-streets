package prompt_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

// ttyIO returns an IOStreams whose In holds the given canned key bytes and
// whose stdin TTY flag is on, so Live methods don't short-circuit with
// ErrNotTTY. The bytes are parsed by bubbletea's input reader as key
// events ("\r" = Enter, "\x03" = Ctrl+C, etc.).
func ttyIO(t *testing.T, input string) *iostreams.IOStreams {
	t.Helper()
	t.Setenv("TERM", "xterm-256color")
	io, in, _, _ := iostreams.Test()
	in.WriteString(input)
	io.SetStdinTTY(true)
	return io
}

// scriptedCtx bounds a scripted-input test. If the canned bytes fail to
// complete the form, the tea program waits forever after input EOF — the
// deadline turns that into a fast, diagnosable failure instead of a
// go-test-timeout stall.
func scriptedCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
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

func TestLiveRefusesDumbTerminal(t *testing.T) {
	// huh silently flips to its accessible mode on TERM=dumb, which
	// breaks the ctx and abort contracts — the live impl must refuse
	// instead, even though stdin claims to be a TTY.
	t.Setenv("TERM", "dumb")
	io, _, _, _ := iostreams.Test()
	io.SetStdinTTY(true)
	p := prompt.NewLive(io)
	_, err := p.Confirm(context.Background(), "?", false)
	if !errors.Is(err, prompt.ErrNotTTY) {
		t.Errorf("got %v, want ErrNotTTY", err)
	}
}

func TestLiveConfirmScripted(t *testing.T) {
	cases := []struct {
		name  string
		input string
		def   bool
		want  bool
	}{
		{"y-submits-yes", "y", false, true},
		{"n-submits-no", "n", true, false},
		{"enter-accepts-default-true", "\r", true, true},
		{"enter-accepts-default-false", "\r", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := prompt.NewLive(ttyIO(t, c.input))
			got, err := p.Confirm(scriptedCtx(t), "?", c.def)
			if err != nil {
				t.Fatalf("Confirm: %v", err)
			}
			if got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestLiveConfirmCtrlCAborts(t *testing.T) {
	p := prompt.NewLive(ttyIO(t, "\x03"))
	_, err := p.Confirm(scriptedCtx(t), "?", false)
	if !errors.Is(err, prompt.ErrAborted) {
		t.Errorf("got %v, want ErrAborted", err)
	}
}

func TestLiveConfirmHonorsCtxCancel(t *testing.T) {
	t.Setenv("TERM", "xterm-256color") // TERM=dumb in the env would trip the dumb-terminal refusal first
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

// hangReader blocks forever. tea's input goroutine does enter Read before
// noticing the cancelled ctx; the ctx-kill shutdown path abandons the
// read without waiting, so each use parks one goroutine for the life of
// the test binary — the caller still sees ctx.Err() immediately via the
// live impl's ctx check.
type hangReader struct{}

func (hangReader) Read(_ []byte) (int, error) { select {} }
func (hangReader) Close() error               { return nil }

func TestLiveInputScripted(t *testing.T) {
	cases := []struct {
		name  string
		input string
		def   string
		want  string
	}{
		{"enter-returns-default", "\r", "fallback", "fallback"},
		{"typed-text-appends-to-default", "-x\r", "base", "base-x"},
		{"typed-text-no-default", "hi\r", "", "hi"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := prompt.NewLive(ttyIO(t, c.input))
			got, err := p.Input(scriptedCtx(t), "?", c.def)
			if err != nil {
				t.Fatalf("Input: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestLivePasswordScripted(t *testing.T) {
	io, in, _, errOut := iostreams.Test()
	t.Setenv("TERM", "xterm-256color")
	in.WriteString("hunter2\r")
	io.SetStdinTTY(true)
	p := prompt.NewLive(io)
	got, err := p.Password(scriptedCtx(t), "secret?")
	if err != nil {
		t.Fatalf("Password: %v", err)
	}
	if got != "hunter2" {
		t.Errorf("got %q, want hunter2", got)
	}
	if strings.Contains(errOut.String(), "hunter2") {
		t.Error("password echoed to output")
	}
}

func TestLiveSelectScripted(t *testing.T) {
	// Down arrow (or j) moves the cursor; Enter submits. Cursor starts
	// on the first option.
	cases := []struct {
		name  string
		input string
		want  int
	}{
		{"enter-picks-first", "\r", 0},
		{"j-moves-down", "j\r", 1},
		{"jj-picks-third", "jj\r", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := prompt.NewLive(ttyIO(t, c.input))
			got, err := p.Select(scriptedCtx(t), "Pick", []string{"a", "b", "c"})
			if err != nil {
				t.Fatalf("Select: %v", err)
			}
			if got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}

func TestLiveSelectRejectsEmptyOptions(t *testing.T) {
	io, _, _, _ := iostreams.Test()
	t.Setenv("TERM", "xterm-256color")
	io.SetStdinTTY(true)
	p := prompt.NewLive(io)
	_, err := p.Select(context.Background(), "Pick", nil)
	if err == nil || !strings.Contains(err.Error(), "at least one option") {
		t.Errorf("got %v, want options-required error", err)
	}
}

func TestLiveMultiSelectScripted(t *testing.T) {
	// x (or space) toggles the option under the cursor; Enter submits.
	cases := []struct {
		name  string
		input string
		want  []int
	}{
		{"none-selected", "\r", nil},
		{"first-and-third", "xjjx\r", []int{0, 2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := prompt.NewLive(ttyIO(t, c.input))
			got, err := p.MultiSelect(scriptedCtx(t), "Pick many", []string{"a", "b", "c"})
			if err != nil {
				t.Fatalf("MultiSelect: %v", err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Fatalf("got %v, want %v", got, c.want)
				}
			}
		})
	}
}

func TestLiveMultiSelectRejectsEmptyOptions(t *testing.T) {
	io, _, _, _ := iostreams.Test()
	t.Setenv("TERM", "xterm-256color")
	io.SetStdinTTY(true)
	p := prompt.NewLive(io)
	_, err := p.MultiSelect(context.Background(), "Pick", nil)
	if err == nil || !strings.Contains(err.Error(), "at least one option") {
		t.Errorf("got %v, want options-required error", err)
	}
}
