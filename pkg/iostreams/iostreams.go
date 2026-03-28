package iostreams

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"time"
)

type IOStreams struct {
	In     io.ReadCloser
	Out    io.Writer
	ErrOut io.Writer

	isTTY          bool
	isColorEnabled bool

	colorScheme *ColorScheme
}

func System() *IOStreams {
	tty := isTerminal(os.Stdout)
	colorEnabled := tty && os.Getenv("NO_COLOR") == ""

	return &IOStreams{
		In:             os.Stdin,
		Out:            os.Stdout,
		ErrOut:         os.Stderr,
		isTTY:          tty,
		isColorEnabled: colorEnabled,
	}
}

func Test() (*IOStreams, *bytes.Buffer, *bytes.Buffer, *bytes.Buffer) {
	in := &bytes.Buffer{}
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	return &IOStreams{
		In:             io.NopCloser(in),
		Out:            out,
		ErrOut:         errOut,
		isTTY:          false,
		isColorEnabled: false,
	}, in, out, errOut
}

func (s *IOStreams) IsTTY() bool {
	return s.isTTY
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

func isTerminal(w io.Writer) bool {
	if f, ok := w.(*os.File); ok {
		fi, err := f.Stat()
		if err != nil {
			return false
		}
		return (fi.Mode() & os.ModeCharDevice) != 0
	}
	return false
}
