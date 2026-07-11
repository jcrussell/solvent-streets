package tui

import (
	"context"
	"fmt"
	"io"

	tea "charm.land/bubbletea/v2"
)

// WorkFunc is the signature for the background work function.
// It receives a context that is cancelled when the TUI quits or the user
// hits ctrl+c, an io.Writer for normal output, an io.Writer for
// warnings/stderr, and a PhaseNotifier for step transitions. Implementations
// must thread ctx into cancellable operations so a ctrl+c actually stops work.
type WorkFunc func(ctx context.Context, out io.Writer, errOut io.Writer, notify PhaseNotifier) error

// Run executes a bubbletea step-checklist TUI. It derives a cancelable child
// context, spawns workFn in a goroutine, and blocks until the TUI exits.
//
// Because bubbletea's raw mode disables ISIG, a signal.NotifyContext on the
// caller's ctx never fires on ctrl+c; the model maps ctrl+c to tea.Quit +
// "interrupted" instead. So on any exit (success, error, or ctrl+c) we cancel
// the child ctx to stop the worker's ctx-aware operations, then WAIT for the
// goroutine to return before Run returns — otherwise, under `all compute`, the
// next resource's TUI would start while the abandoned goroutine still writes to
// the same city store.
func Run(ctx context.Context, label string, steps []Step, done DoneConfig, workFn WorkFunc) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	model := NewStepModel(label, steps, done)
	p := tea.NewProgram(model, tea.WithoutCatchPanics())

	writer := NewProgressWriter(p)
	warnWriter := NewWarnWriter(p)
	notify := &TUINotifier{Program: p}

	// workDone is closed when the work goroutine returns; Run joins on it
	// before returning (goroutine-exit-path).
	workDone := make(chan struct{})
	go func() {
		defer close(workDone)
		defer func() {
			if r := recover(); r != nil {
				writer.Flush()
				warnWriter.Flush()
				p.Send(allDoneMsg{err: fmt.Errorf("panic: %v", r)})
			}
		}()
		workErr := workFn(ctx, writer, warnWriter, notify)
		writer.Flush()
		warnWriter.Flush()
		p.Send(allDoneMsg{err: workErr})
	}()

	finalModel, runErr := p.Run()

	// The TUI has exited (quit, error, or ctrl+c). Cancel the child ctx so any
	// still-running work observes cancellation, then join the goroutine so it
	// cannot keep writing to the DB after Run returns.
	cancel()
	<-workDone

	if runErr != nil {
		return runErr
	}

	if m, ok := finalModel.(StepModel); ok && m.Err() != nil {
		return m.Err()
	}

	return nil
}
