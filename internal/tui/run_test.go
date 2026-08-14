package tui

import (
	"errors"
	"io"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

type boomMsg struct{}

type panicModel struct{}

func (panicModel) Init() tea.Cmd { return func() tea.Msg { return boomMsg{} } }

func (m panicModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(boomMsg); ok {
		panic("boom in Update")
	}
	return m, nil
}

func (m panicModel) View() tea.View { return tea.NewView("") }

// TestProgramCatchesModelPanic pins am3l: Run constructs the Bubbletea
// program WITHOUT tea.WithoutCatchPanics, so a panic in the model's Update
// (which runs on the event loop) is caught by Bubbletea — it restores the
// terminal and returns an error — instead of propagating and wedging the
// terminal. This mirrors run.go's construction; a regression that re-adds
// WithoutCatchPanics would let the panic escape and fail this test.
func TestProgramCatchesModelPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("model panic escaped Run instead of being caught: %v", r)
		}
	}()

	p := tea.NewProgram(
		panicModel{},
		tea.WithInput(strings.NewReader("")),
		tea.WithOutput(io.Discard),
		tea.WithoutRenderer(),
	)
	if _, err := p.Run(); err == nil {
		t.Fatal("expected Run to return an error after a model panic")
	} else if !errors.Is(err, tea.ErrProgramPanic) {
		t.Errorf("expected ErrProgramPanic, got %v", err)
	}
}
