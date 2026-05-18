package iostreams

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"time"
)

// IOStreams wraps the three standard streams plus per-stream TTY flags so
// commands can be tested with in-memory buffers and so output behavior
// (colors, progress, prompts) keys off explicit predicates instead of
// scattered isatty calls. The single allowed entry point that touches
// os.Stdin/os.Stdout/os.Stderr in this codebase is System(); everything
// else accepts an *IOStreams and writes through it. Each stream carries
// its own TTY flag because they can independently be a terminal, a file,
// or a pipe (e.g. `pvmt list | less` has stdout=pipe but stderr=tty).
//
// Stream-routing contract (byob-iostreams.3):
//
//   - Out  — command DATA: the rows, table, JSON, or value a user might
//     pipe into jq/awk/wc. If removing the print would change the meaning
//     of `cmd | wc -l`, it belongs on Out.
//   - ErrOut — chatter: status lines ("Fetching..."), progress, warnings,
//     non-fatal errors, prompts, and any human-readable hint that is not
//     itself the command's data. Prompts always go to ErrOut because they
//     pair with reads from In, and In is usually the pipe.
//
// Rule of thumb: scripting consumers run `cmd | …` and expect Out to
// contain only data. Humans see ErrOut interleaved on their terminal
// regardless. When in doubt, route to ErrOut — that strictly preserves
// pipe semantics. Empty-state messages ("No cities in database") also
// go to ErrOut so `cmd | wc -l` is 0 when there is nothing to emit.
type IOStreams struct {
	In io.ReadCloser
	// Out carries command DATA only — see the IOStreams type comment for
	// the Out vs ErrOut routing contract (byob-iostreams.3). Chatter,
	// warnings, progress, and prompts go to ErrOut.
	Out io.Writer
	// ErrOut carries chatter: status, progress, warnings, hints, prompts.
	// Anything whose absence would not change the meaning of `cmd | wc -l`
	// belongs here. See the IOStreams type comment (byob-iostreams.3).
	ErrOut io.Writer

	stdinIsTTY     bool
	stdoutIsTTY    bool
	stderrIsTTY    bool
	isColorEnabled bool

	colorScheme *ColorScheme
}

func System() *IOStreams {
	stdinTTY := isTerminal(os.Stdin)
	stdoutTTY := isTerminal(os.Stdout)
	stderrTTY := isTerminal(os.Stderr)
	return &IOStreams{
		In:             os.Stdin,
		Out:            os.Stdout,
		ErrOut:         os.Stderr,
		stdinIsTTY:     stdinTTY,
		stdoutIsTTY:    stdoutTTY,
		stderrIsTTY:    stderrTTY,
		isColorEnabled: shouldEnableColor(stdoutTTY, os.LookupEnv),
	}
}

// shouldEnableColor returns true when ANSI color should be emitted: stdout
// must be a TTY and NO_COLOR must be unset. Presence of NO_COLOR (regardless
// of value, including empty string) disables color per https://no-color.org.
func shouldEnableColor(isTTY bool, lookupEnv func(string) (string, bool)) bool {
	if !isTTY {
		return false
	}
	_, noColor := lookupEnv("NO_COLOR")
	return !noColor
}

// Test returns an IOStreams backed by in-memory buffers with every TTY flag
// false and color disabled. Use it from tests that compare command output
// against golden strings; the lack of ANSI escapes is the load-bearing
// guarantee.
func Test() (*IOStreams, *bytes.Buffer, *bytes.Buffer, *bytes.Buffer) {
	in := &bytes.Buffer{}
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	return &IOStreams{
		In:             io.NopCloser(in),
		Out:            out,
		ErrOut:         errOut,
		stdinIsTTY:     false,
		stdoutIsTTY:    false,
		stderrIsTTY:    false,
		isColorEnabled: false,
	}, in, out, errOut
}

// IsStdinTTY reports whether In is a terminal. The Prompter (byob-prompter)
// consults this to decide whether to prompt at all — when stdin is a pipe,
// prompting would block on input that's never coming.
func (s *IOStreams) IsStdinTTY() bool { return s.stdinIsTTY }

// IsStdoutTTY reports whether Out is a terminal. Drives color and human-vs-
// machine output formatting decisions.
func (s *IOStreams) IsStdoutTTY() bool { return s.stdoutIsTTY }

// IsStderrTTY reports whether ErrOut is a terminal. Drives whether progress
// indicators and prompts should render fancy interactive output.
func (s *IOStreams) IsStderrTTY() bool { return s.stderrIsTTY }

// IsTTY is shorthand for IsStdoutTTY — kept because most callers' decisions
// (color, table padding, relative-time suffix) key off stdout specifically.
func (s *IOStreams) IsTTY() bool {
	return s.stdoutIsTTY
}

// SetTTY overrides the stdout TTY flag (useful in tests that want to
// exercise the TTY rendering paths). Does not affect stdin or stderr.
func (s *IOStreams) SetTTY(v bool) {
	s.stdoutIsTTY = v
}

// SetStdinTTY overrides the stdin TTY flag (useful in tests that exercise
// prompting behavior).
func (s *IOStreams) SetStdinTTY(v bool) {
	s.stdinIsTTY = v
}

// SetStderrTTY overrides the stderr TTY flag (useful in tests that exercise
// progress-indicator behavior).
func (s *IOStreams) SetStderrTTY(v bool) {
	s.stderrIsTTY = v
}

func (s *IOStreams) IsColorEnabled() bool {
	return s.isColorEnabled
}

func (s *IOStreams) ColorScheme() *ColorScheme {
	if s.colorScheme == nil {
		s.colorScheme = NewColorScheme(s.isColorEnabled)
	}
	return s.colorScheme
}

// RelativeTime formats a time as a human-readable relative duration (e.g., "3 hours ago").
func RelativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}

// FormatTimestamp renders an RFC3339 timestamp string for display. On a TTY
// it appends a relative-time suffix; otherwise it returns the raw value.
// Empty input renders as "never".
func FormatTimestamp(raw string, isTTY bool) string {
	if raw == "" {
		return "never"
	}
	if isTTY {
		t, _ := time.Parse(time.RFC3339, raw)
		return fmt.Sprintf("%s (%s)", raw, RelativeTime(t))
	}
	return raw
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
