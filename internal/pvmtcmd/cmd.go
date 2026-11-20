package pvmtcmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jcrussell/solvent-streets/internal/hint"
	"github.com/jcrussell/solvent-streets/pkg/cmd/factory"
	"github.com/jcrussell/solvent-streets/pkg/cmd/root"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
)

func Main() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The first signal cancels ctx. Unregister right away so the next one
	// gets the default disposition and kills the process: while the
	// NotifyContext registration is live, Go diverts every SIGINT into ctx,
	// so a command stuck in a loop that never checks ctx could not be
	// interrupted at all.
	go func() {
		<-ctx.Done()
		stop()
	}()

	f := factory.New()
	// Release the shared DB handle on shutdown — but only if the run actually
	// opened it (CloseRootDB is a no-op otherwise), so DB-free commands like
	// --version don't create the database just to close it.
	defer func() {
		if f.CloseRootDB != nil {
			_ = f.CloseRootDB()
		}
	}()
	return run(ctx, f, os.Args[1:])
}

// run executes the command tree for args and maps the outcome to a process
// exit code. Split from Main so tests drive the real root and the real
// exit-code mapping without signals or the process environment.
func run(ctx context.Context, f *cmdutil.Factory, args []string) int {
	rootCmd := root.NewCmdRoot(f)
	root.RefuseUnknownShells(rootCmd)
	rootCmd.SetArgs(args)
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		return exitCode(err, f.IOStreams.ErrOut)
	}
	return 0
}

func exitCode(err error, errOut io.Writer) int {
	// A declined confirmation prompt is a deliberate user choice, so it is a
	// success: exit 0. An INTERRUPT is not. Mapping context.Canceled to 0 here
	// too would make `pvmt check-site && deploy` and `pvmt export && deploy`
	// treat a half-done run as a green gate — the gate would report ready for
	// checks that never ran, and export would exit 0 with a partially written
	// site after --clean had already removed the previous one.
	// Cancellation falls through to the generic arm
	// below: printed, exit 1.
	//
	// Deliberately NOT a dedicated quiet `context.Canceled -> 1` arm. A
	// multi-city run that is interrupted after real per-city failures returns
	// errors.Join(failures..., ctx.Err()) from cmdutil.ForEachCity, so an arm
	// matching on errors.Is and printing a fixed "interrupted" line would
	// swallow those failures. Falling through costs a blunt
	// "Error: context canceled" on a bare Ctrl-C and hides nothing.
	if errors.Is(err, cmdutil.ErrCancel) {
		return 0
	}
	err = classifyUsageError(err)
	if _, ok := errors.AsType[*cmdutil.FlagError](err); ok {
		printError(errOut, err)
		return 2
	}
	if cmdutil.OnlyPrereqs(err) {
		printError(errOut, err)
		return 3
	}
	if errors.Is(err, cmdutil.ErrNoResults) {
		return 3
	}
	if errors.Is(err, cmdutil.ErrSilent) {
		return 1
	}
	printError(errOut, err)
	return 1
}

// classifyUsageError wraps untyped cobra errors we recognize as user
// errors so they map to exit code 2 alongside flag-parse errors. Cobra
// has no public sentinels for these paths — string-prefix matching is
// the documented escape hatch. Covered:
//   - "unknown command ..." from cobra's command lookup.
//   - "if any flags in the group ..." from MarkFlagsMutuallyExclusive
//     and MarkFlagsRequiredTogether.
//   - "at least one of the flags in the group ..." from
//     MarkFlagsOneRequired.
//   - "required flag(s) ... not set" from MarkFlagRequired.
//   - "accepts ... arg(s) ..." from ExactArgs/RangeArgs/MaximumNArgs.
//   - "requires at least ... arg(s)" from MinimumNArgs.
//
// Without the arg-count / required-flag prefixes, e.g. `snapshots prune`
// (missing --keep) and `snapshots delete` (missing id) exited 1 while a bad
// --keep value exited 2 — inconsistent exit codes for the same class of
// user error.
func classifyUsageError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*cmdutil.FlagError](err); ok {
		return err
	}
	msg := err.Error()
	if strings.HasPrefix(msg, "unknown command ") ||
		strings.HasPrefix(msg, "if any flags in the group ") ||
		strings.HasPrefix(msg, "at least one of the flags in the group ") ||
		strings.HasPrefix(msg, "required flag(s) ") ||
		strings.HasPrefix(msg, "accepts ") ||
		strings.HasPrefix(msg, "requires at least ") {
		return &cmdutil.FlagError{Err: err}
	}
	return err
}

func printError(w io.Writer, err error) {
	fmt.Fprintf(w, "Error: %s\n", err)
	hint.Fprint(w, err)
}
