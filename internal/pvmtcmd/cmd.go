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
	var flagErr *cmdutil.FlagError
	if errors.As(err, &flagErr) {
		printError(errOut, err)
		return 2
	}
	if errors.Is(err, cmdutil.ErrNoResults) {
		return 3
	}
	printError(errOut, err)
	return 1
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
