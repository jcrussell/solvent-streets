package iostreams

import (
	"io"
	"os"
	"os/exec"
	"strings"
)

type pagerProcess struct {
	cmd *exec.Cmd
	in  io.WriteCloser
}

// startPager launches the pager command. Note: strings.Fields splitting
// does not handle quoted arguments (e.g., 'less -R -X' works, but commands
// with quoted paths will not). This matches our simple use case.
func startPager(command string, out io.Writer) *pagerProcess {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return nil
	}

	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Stdout = out
	cmd.Stderr = os.Stderr

	in, err := cmd.StdinPipe()
	if err != nil {
		return nil
	}

	if err := cmd.Start(); err != nil {
		return nil
	}

	return &pagerProcess{cmd: cmd, in: in}
}

func (p *pagerProcess) stop() {
	_ = p.in.Close()
	_ = p.cmd.Wait()
}
