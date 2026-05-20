package prompt

import (
	"context"
	"fmt"
)

// Stub is a scripted Prompter for tests (byob-prompter.4). Each method
// pops the head of its per-method FIFO; running out panics so a missing
// expectation surfaces immediately instead of returning the zero value
// and silently passing the test.
//
// The stub deliberately ignores ctx — tests don't cancel mid-prompt —
// and does not record call order across methods. When ordering across
// different methods matters, the test should assert call order through
// a separate side-effect (e.g. checking what the command emitted).
type Stub struct {
	Confirms     []bool
	Inputs       []string
	Passwords    []string
	Selects      []int
	MultiSelects [][]int
}

func (s *Stub) Confirm(_ context.Context, msg string, _ bool) (bool, error) {
	if len(s.Confirms) == 0 {
		panic(fmt.Sprintf("prompt.Stub: no Confirms queued for %q", msg))
	}
	v := s.Confirms[0]
	s.Confirms = s.Confirms[1:]
	return v, nil
}

func (s *Stub) Input(_ context.Context, msg, _ string) (string, error) {
	if len(s.Inputs) == 0 {
		panic(fmt.Sprintf("prompt.Stub: no Inputs queued for %q", msg))
	}
	v := s.Inputs[0]
	s.Inputs = s.Inputs[1:]
	return v, nil
}

func (s *Stub) Password(_ context.Context, msg string) (string, error) {
	if len(s.Passwords) == 0 {
		panic(fmt.Sprintf("prompt.Stub: no Passwords queued for %q", msg))
	}
	v := s.Passwords[0]
	s.Passwords = s.Passwords[1:]
	return v, nil
}

func (s *Stub) Select(_ context.Context, msg string, _ []string) (int, error) {
	if len(s.Selects) == 0 {
		panic(fmt.Sprintf("prompt.Stub: no Selects queued for %q", msg))
	}
	v := s.Selects[0]
	s.Selects = s.Selects[1:]
	return v, nil
}

func (s *Stub) MultiSelect(_ context.Context, msg string, _ []string) ([]int, error) {
	if len(s.MultiSelects) == 0 {
		panic(fmt.Sprintf("prompt.Stub: no MultiSelects queued for %q", msg))
	}
	v := s.MultiSelects[0]
	s.MultiSelects = s.MultiSelects[1:]
	return v, nil
}
