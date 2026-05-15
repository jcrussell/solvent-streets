package build

import (
	"fmt"
	"runtime"
)

type Info struct {
	Version   string
	Commit    string
	Date      string
	GoVersion string
	OS        string
	Arch      string
}

func Current() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
}

func (i Info) Short() string {
	return fmt.Sprintf("%s (commit %s)", i.Version, i.Commit)
}

func (i Info) Full() string {
	return fmt.Sprintf("pvmt %s\n  commit:  %s\n  built:   %s\n  go:      %s\n  os/arch: %s/%s\n",
		i.Version, i.Commit, i.Date, i.GoVersion, i.OS, i.Arch)
}
