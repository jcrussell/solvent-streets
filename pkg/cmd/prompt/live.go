package prompt

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/term"

	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

// NewLive returns a Prompter that reads from io.In and writes prompts to
// io.ErrOut (chatter, per the iostreams routing contract). It refuses to
// prompt when stdin is not a TTY, returning ErrNotTTY; callers should
// short-circuit on --yes before calling Confirm rather than mapping
// ErrNotTTY to a fallback default — silently confirming because stdin
// happened not to be a TTY is exactly the failure mode byob-prompter.3
// is meant to prevent.
func NewLive(io *iostreams.IOStreams) Prompter { return &live{io: io} }

type live struct{ io *iostreams.IOStreams }

func (p *live) Confirm(ctx context.Context, msg string, def bool) (bool, error) {
	if !p.io.IsStdinTTY() {
		return false, ErrNotTTY
	}
	suffix := "[y/N]"
	if def {
		suffix = "[Y/n]"
	}
	line, err := readLine(ctx, p.io.In, p.io.ErrOut, fmt.Sprintf("%s %s ", msg, suffix))
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return def, nil
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, fmt.Errorf("invalid response %q (expected y/n)", line)
	}
}

func (p *live) Input(ctx context.Context, msg, def string) (string, error) {
	if !p.io.IsStdinTTY() {
		return "", ErrNotTTY
	}
	prompt := msg
	if def != "" {
		prompt = fmt.Sprintf("%s [%s]", msg, def)
	}
	line, err := readLine(ctx, p.io.In, p.io.ErrOut, prompt+" ")
	if err != nil {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return def, nil
	}
	return line, nil
}

func (p *live) Password(ctx context.Context, msg string) (string, error) {
	if !p.io.IsStdinTTY() {
		return "", ErrNotTTY
	}
	// term.ReadPassword needs a real file descriptor. IsStdinTTY()
	// already implies In is an *os.File, but we guard with a type
	// assertion so a test that flips SetStdinTTY(true) on an in-memory
	// buffer fails loudly instead of segfaulting on Fd().
	f, ok := p.io.In.(interface{ Fd() uintptr })
	if !ok {
		return "", errors.New("prompt: stdin does not expose Fd; cannot read password without echo")
	}
	fmt.Fprint(p.io.ErrOut, msg+" ")
	type result struct {
		s   string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		b, err := term.ReadPassword(f.Fd())
		ch <- result{s: string(b), err: err}
	}()
	select {
	case r := <-ch:
		fmt.Fprintln(p.io.ErrOut)
		return r.s, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (p *live) Select(ctx context.Context, msg string, options []string) (int, error) {
	if !p.io.IsStdinTTY() {
		return 0, ErrNotTTY
	}
	if len(options) == 0 {
		return 0, errors.New("prompt: Select requires at least one option")
	}
	fmt.Fprintln(p.io.ErrOut, msg)
	for i, o := range options {
		fmt.Fprintf(p.io.ErrOut, "  %d) %s\n", i+1, o)
	}
	line, err := readLine(ctx, p.io.In, p.io.ErrOut, fmt.Sprintf("Enter 1-%d: ", len(options)))
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(options) {
		return 0, fmt.Errorf("invalid selection %q (expected 1-%d)", strings.TrimSpace(line), len(options))
	}
	return n - 1, nil
}

func (p *live) MultiSelect(ctx context.Context, msg string, options []string) ([]int, error) {
	if !p.io.IsStdinTTY() {
		return nil, ErrNotTTY
	}
	if len(options) == 0 {
		return nil, errors.New("prompt: MultiSelect requires at least one option")
	}
	fmt.Fprintln(p.io.ErrOut, msg)
	for i, o := range options {
		fmt.Fprintf(p.io.ErrOut, "  %d) %s\n", i+1, o)
	}
	line, err := readLine(ctx, p.io.In, p.io.ErrOut, fmt.Sprintf("Enter comma-separated 1-%d (blank for none): ", len(options)))
	if err != nil {
		return nil, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, nil
	}
	parts := strings.Split(line, ",")
	out := make([]int, 0, len(parts))
	seen := make(map[int]bool, len(parts))
	for _, raw := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || n < 1 || n > len(options) {
			return nil, fmt.Errorf("invalid selection %q (expected 1-%d)", strings.TrimSpace(raw), len(options))
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n-1)
	}
	return out, nil
}

// readLine prints prompt to w, then reads a single newline-terminated
// line from r in a goroutine so the read can be abandoned on ctx
// cancellation. The read goroutine leaks until the user types something
// (or stdin is closed), matching the byob-prompter.2 contract — caller
// gets ctx.Err() immediately, which is what matters for shutdown.
func readLine(ctx context.Context, r io.Reader, w io.Writer, prompt string) (string, error) {
	fmt.Fprint(w, prompt)
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		s := bufio.NewScanner(r)
		s.Buffer(make([]byte, 0, 4096), 1<<20)
		if !s.Scan() {
			if err := s.Err(); err != nil {
				ch <- result{err: err}
				return
			}
			ch <- result{err: io.EOF}
			return
		}
		ch <- result{line: s.Text()}
	}()
	select {
	case res := <-ch:
		return res.line, res.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Compile-time guard that os.Stdin satisfies the Fd interface used in
// Password. If x/term ever drops the int-fd API, this will fail to
// compile in the same place as the runtime assertion.
var _ interface{ Fd() uintptr } = (*os.File)(nil)
