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

	"github.com/jcrussell/solvent-streets/pkg/cmd/factory"
	"github.com/jcrussell/solvent-streets/pkg/cmd/root"
	"github.com/jcrussell/solvent-streets/pkg/cmdutil"
)

func Main() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	f := factory.New()
	// Release the shared DB handle on shutdown — but only if the run actually
	// opened it (CloseRootDB is a no-op otherwise), so DB-free commands like
	// --version don't create the database just to close it.
	defer func() {
		if f.CloseRootDB != nil {
			_ = f.CloseRootDB()
		}
	}()
	rootCmd := root.NewCmdRoot(f)
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		return exitCode(err, f.IOStreams.ErrOut)
	}
	return 0
}

func exitCode(err error, errOut io.Writer) int {
	// A declined confirmation prompt is a deliberate user choice, so it is a
	// success: exit 0. An INTERRUPT is not. Mapping context.Canceled to 0 here
	// too meant `pvmt check-site && deploy` and `pvmt export && deploy` treated
	// a half-done run as a green gate — the gate reported ready for checks that
	// never ran, and export exited 0 with a partially written site after
	// SafeCleanDir had already removed the previous one (solvent-streets-kh5n).
	// Cancellation now falls through to the generic arm below: printed, exit 1.
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
	var flagErr *cmdutil.FlagError
	if errors.As(err, &flagErr) {
		printError(errOut, err)
		return 2
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
//   - "unknown command ..." from cobra's command lookup (byob-errors.4).
//   - "if any flags in the group ..." from MarkFlagsMutuallyExclusive
//     and MarkFlagsRequiredTogether (byob-command-shape.6).
//   - "at least one of the flags in the group ..." from
//     MarkFlagsOneRequired (byob-command-shape.6).
//   - "required flag(s) ... not set" from MarkFlagRequired.
//   - "accepts ... arg(s) ..." from ExactArgs/RangeArgs/MaximumNArgs.
//   - "requires at least ... arg(s)" from MinimumNArgs.
//
// Without the arg-count / required-flag prefixes, e.g. `snapshots prune`
// (missing --keep) and `snapshots rm` (missing id) exited 1 while a bad
// --keep value exited 2 — inconsistent exit codes for the same class of
// user error (solvent-streets-8407).
func classifyUsageError(err error) error {
	if err == nil {
		return nil
	}
	var flagErr *cmdutil.FlagError
	if errors.As(err, &flagErr) {
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
	var hint *cmdutil.ErrHint
	if errors.As(err, &hint) && hint.Hint != "" {
		lines := strings.Split(hint.Hint, "\n")
		fmt.Fprintf(w, "hint: %s\n", lines[0])
		for _, l := range lines[1:] {
			fmt.Fprintf(w, "      %s\n", l)
		}
	}
}
