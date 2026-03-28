package tui

import (
	"bytes"
	"sync"

	tea "charm.land/bubbletea/v2"
)

// MsgWriter implements io.Writer and sends each complete line as a
// tea.Msg to the bubbletea program. Partial lines are buffered until a
// newline is received. The wrap function converts a line string into
// the desired message type.
type MsgWriter[T any] struct {
	mu      sync.Mutex
	program *tea.Program
	buf     []byte
	wrap    func(string) T
}

// NewProgressWriter creates a MsgWriter that sends lines as ProgressMsg.
func NewProgressWriter(p *tea.Program) *MsgWriter[ProgressMsg] {
	return &MsgWriter[ProgressMsg]{
		program: p,
		wrap:    func(s string) ProgressMsg { return ProgressMsg{Line: s} },
	}
}

// NewWarnWriter creates a MsgWriter that sends lines as WarnMsg.
func NewWarnWriter(p *tea.Program) *MsgWriter[WarnMsg] {
	return &MsgWriter[WarnMsg]{
		program: p,
		wrap:    func(s string) WarnMsg { return WarnMsg{Line: s} },
	}
}

// Write implements io.Writer. It buffers input and sends each complete line
// as a message to the bubbletea program.
func (w *MsgWriter[T]) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	n := len(p)
	w.buf = append(w.buf, p...)

	for {
		idx := bytes.IndexByte(w.buf, '\n')
		if idx < 0 {
			break
		}
		line := string(w.buf[:idx])
		w.buf = w.buf[idx+1:]

		if line != "" {
			w.program.Send(w.wrap(line))
		}
	}

	return n, nil
}

// Flush sends any remaining partial line in the buffer.
func (w *MsgWriter[T]) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.buf) > 0 {
		line := string(w.buf)
		w.buf = nil
		w.program.Send(w.wrap(line))
	}
}
