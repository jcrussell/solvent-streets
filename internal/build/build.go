package build

// Sentinel values for build metadata that ldflags did not populate. Consumers
// should compare against these constants (not raw string literals) so the
// "no info" check stays consistent across the codebase.
const (
	VersionDev  = "dev"
	CommitNone  = "none"
	DateUnknown = "unknown"
)

var (
	Version = VersionDev
	Commit  = CommitNone
	Date    = DateUnknown
)
