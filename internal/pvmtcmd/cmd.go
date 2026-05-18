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
	rootCmd := root.NewCmdRoot(f)
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		return exitCode(err, os.Stderr)
	}
	return 0
}

func exitCode(err error, errOut io.Writer) int {
	if errors.Is(err, cmdutil.ErrCancel) || errors.Is(err, context.Canceled) {
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
		strings.HasPrefix(msg, "at least one of the flags in the group ") {
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
