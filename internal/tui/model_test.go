package tui

import (
	"errors"
	"testing"
)

// TestStepModel_PhaseTransitions pins the state-machine contract that
// drives the compute progress UI. PhaseStart on a pending step flips it
// to active; PhaseDone (with nil err) flips it to done; PhaseDone with
// an err flips it to failed and stores the error string in Detail.
// Regression for solvent-streets-v382: a transition that silently
// dropped a state would leave operators watching a frozen UI.
func TestStepModel_PhaseTransitions(t *testing.T) {
	m := NewStepModel("compute", []Step{
		{Name: "step-0"},
		{Name: "step-1"},
		{Name: "step-2"},
	}, DoneConfig{SuccessMsg: "done"})

	m.handlePhaseStart(PhaseStartMsg{Phase: 0})
	if got := m.steps[0].Status; got != StepActive {
		t.Errorf("after PhaseStart(0): step.Status = %v; want StepActive", got)
	}

	m.handlePhaseDone(PhaseDoneMsg{Phase: 0, Err: nil})
	if got := m.steps[0].Status; got != StepDone {
		t.Errorf("after PhaseDone(0, nil): step.Status = %v; want StepDone", got)
	}

	boom := errors.New("boom")
	m.handlePhaseDone(PhaseDoneMsg{Phase: 1, Err: boom})
	if got := m.steps[1].Status; got != StepFailed {
		t.Errorf("after PhaseDone(1, err): step.Status = %v; want StepFailed", got)
	}
	if got := m.steps[1].Detail; got != "boom" {
		t.Errorf("step Detail after failure = %q; want %q", got, "boom")
	}
}

// TestStepModel_PhaseStart_OnlyOnPending pins idempotence: PhaseStart on
// an already-done step must not reactivate it. Re-activating a completed
// step would visibly flicker the UI.
func TestStepModel_PhaseStart_OnlyOnPending(t *testing.T) {
	m := NewStepModel("compute", []Step{{Name: "step-0"}}, DoneConfig{})
	m.handlePhaseStart(PhaseStartMsg{Phase: 0})
	m.handlePhaseDone(PhaseDoneMsg{Phase: 0, Err: nil})
	m.handlePhaseStart(PhaseStartMsg{Phase: 0})
	if got := m.steps[0].Status; got != StepDone {
		t.Errorf("PhaseStart on done step changed status to %v; want StepDone", got)
	}
}

// TestStepModel_PhaseProgress pins that PhaseProgress mutates Progress
// and Detail on the addressed step and is a no-op for an out-of-range
// index — the caller's loop bound is not load-bearing for safety.
func TestStepModel_PhaseProgress(t *testing.T) {
	m := NewStepModel("compute", []Step{{Name: "step-0"}}, DoneConfig{})
	m.handlePhaseProgress(PhaseProgressMsg{Phase: 0, Percent: 0.42, Detail: "100/240"})
	if got := m.steps[0].Progress; got != 0.42 {
		t.Errorf("Progress = %v; want 0.42", got)
	}
	if got := m.steps[0].Detail; got != "100/240" {
		t.Errorf("Detail = %q; want %q", got, "100/240")
	}

	// Out-of-range index must not panic or grow the slice.
	m.handlePhaseProgress(PhaseProgressMsg{Phase: 99, Percent: 1, Detail: "x"})
	if len(m.steps) != 1 {
		t.Errorf("out-of-range update grew steps slice to %d", len(m.steps))
	}
}

// TestStepModel_ProgressRingBuffer pins the ring-buffer cap on log
// lines. Without this, a long-running ingest would accumulate output
// without bound and the model would balloon.
func TestStepModel_ProgressRingBuffer(t *testing.T) {
	m := NewStepModel("compute", []Step{{Name: "step-0"}}, DoneConfig{})
	for range maxLogLines + 50 {
		m.handleProgress(ProgressMsg{Line: "noise"})
	}
	if got := len(m.logLines); got != maxLogLines {
		t.Errorf("logLines length = %d; want capped at %d", got, maxLogLines)
	}
}

// TestStepModel_WarnRingBuffer mirrors the log buffer cap for warnings.
func TestStepModel_WarnRingBuffer(t *testing.T) {
	m := NewStepModel("compute", []Step{{Name: "step-0"}}, DoneConfig{})
	for range maxWarnLines + 50 {
		m.handleWarn(WarnMsg{Line: "warn"})
	}
	if got := len(m.warnLines); got != maxWarnLines {
		t.Errorf("warnLines length = %d; want capped at %d", got, maxWarnLines)
	}
}

// TestNoopNotifier_AcceptsAllCalls confirms the no-op notifier accepts
// every PhaseNotifier method without panicking. Used by the plain-text
// (non-TTY) code path, so any panic here would silently break --no-tui.
func TestNoopNotifier_AcceptsAllCalls(t *testing.T) {
	var n PhaseNotifier = NoopNotifier{}
	n.PhaseStart(0)
	n.PhaseDone(0, nil)
	n.PhaseDone(0, errors.New("err"))
	n.PhaseProgress(0, 0.5, "")
}

// TestProgressMsg_EventShape pins the JSON-free event payloads emitted
// by the notifier — these are the messages the model expects, so a
// rename or field reorder would break the wiring.
func TestProgressMsg_EventShape(t *testing.T) {
	start := PhaseStartMsg{Phase: 3}
	if start.Phase != 3 {
		t.Errorf("PhaseStartMsg.Phase round-trip = %d; want 3", start.Phase)
	}
	done := PhaseDoneMsg{Phase: 3, Err: errors.New("x")}
	if done.Err == nil || done.Phase != 3 {
		t.Errorf("PhaseDoneMsg round-trip: phase=%d err=%v", done.Phase, done.Err)
	}
	prog := PhaseProgressMsg{Phase: 1, Percent: 0.5, Detail: "d"}
	if prog.Phase != 1 || prog.Percent != 0.5 || prog.Detail != "d" {
		t.Errorf("PhaseProgressMsg round-trip: %+v", prog)
	}
}
