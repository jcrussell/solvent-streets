package pvmtcmd

import (
	"errors"
	"fmt"
	"os"

	"pvmt/pkg/cmd/factory"
	"pvmt/pkg/cmd/root"
	"pvmt/pkg/cmdutil"
)

func Main() int {
	f := factory.New()
	rootCmd := root.NewCmdRoot(f)

	if err := rootCmd.Execute(); err != nil {
		var flagErr *cmdutil.FlagError
		if errors.As(err, &flagErr) {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err)
			return 2
		}
		if errors.Is(err, cmdutil.ErrSilent) {
			return 1
		}
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		return 1
	}
	return 0
}
