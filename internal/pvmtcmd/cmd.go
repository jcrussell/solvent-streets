package pvmtcmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"pvmt/pkg/cmd/factory"
	"pvmt/pkg/cmd/root"
	"pvmt/pkg/cmdutil"
)

func Main() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	f := factory.New()
	rootCmd := root.NewCmdRoot(f)
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		return exitCode(err)
	}
	return 0
}

func exitCode(err error) int {
	if errors.Is(err, cmdutil.ErrCancel) || errors.Is(err, context.Canceled) {
		return 0
	}
	var flagErr *cmdutil.FlagError
	if errors.As(err, &flagErr) {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		return 2
	}
	if errors.Is(err, cmdutil.ErrNoResults) {
		return 3
	}
	if errors.Is(err, cmdutil.ErrPending) {
		return 4
	}
	if errors.Is(err, cmdutil.ErrSilent) {
		return 1
	}
	fmt.Fprintf(os.Stderr, "Error: %s\n", err)
	return 1
}
