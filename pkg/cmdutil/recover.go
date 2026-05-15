package cmdutil

import (
	"fmt"
	"io"
	"runtime/debug"
)

// GuardPanic invokes fn and converts any panic it raises into an error
// return. The panic value plus a stack trace are written to errOut (if
// non-nil) so the user can locate the offending input. The returned
// error wraps only the panic value — callers can attach context with
// fmt.Errorf("processing %s: %w", id, err) without losing the stack
// (which is already in errOut).
//
// Use this anywhere a third-party computation might panic on adversarial
// input — geometry libraries, parsers, anything that takes user data and
// has CGO or unchecked-cast surface area. Wrap the smallest scope you
// can usefully attribute context to.
func GuardPanic(errOut io.Writer, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if errOut != nil {
				fmt.Fprintf(errOut, "panic: %v\n%s\n", r, debug.Stack())
			}
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return fn()
}
