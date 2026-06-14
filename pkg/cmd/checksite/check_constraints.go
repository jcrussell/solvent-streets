package checksite

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	maxFileBytes = 100 << 20 // 100 MiB GitHub per-file hard limit
	warnTreeSize = 1 << 30   // 1 GiB — warn, GitHub Pages soft guidance
)

// checkConstraints enforces the publish size limits and the .nojekyll guard:
// no single file over 100 MB, a present zero-byte .nojekyll, and a reported
// total tree size (warned over 1 GB).
func (r *runner) checkConstraints(s *site) {
	var total int64
	var oversized []string
	err := filepath.WalkDir(s.dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		total += info.Size()
		if info.Size() > maxFileBytes {
			rel, rerr := filepath.Rel(s.dir, path)
			if rerr != nil {
				rel = path
			}
			oversized = append(oversized, rel)
		}
		return nil
	})
	if err != nil {
		r.failf("constraints: size walk failed: %v", err)
		return
	}

	for _, f := range oversized {
		r.failf("constraints: %s exceeds the 100 MB per-file limit", f)
	}
	if len(oversized) == 0 {
		r.passf("constraints: no file exceeds the 100 MB per-file limit")
	}

	r.checkNojekyll(s)

	if total > warnTreeSize {
		r.warnf("constraints: total tree size is %s (over 1 GB)", humanSize(total))
	} else {
		r.passf("constraints: total tree size is %s", humanSize(total))
	}
}

// checkNojekyll fails if .nojekyll is missing or non-zero-byte. GitHub Pages
// only skips Jekyll when the file is present; the zero-byte convention keeps it
// an unambiguous marker rather than accidentally-meaningful content.
func (r *runner) checkNojekyll(s *site) {
	info, err := os.Stat(filepath.Join(s.dir, ".nojekyll"))
	switch {
	case err != nil:
		r.failf("constraints: .nojekyll is missing")
	case info.IsDir():
		r.failf("constraints: .nojekyll is a directory, expected a zero-byte file")
	case info.Size() != 0:
		r.failf("constraints: .nojekyll is %d bytes, expected zero-byte", info.Size())
	default:
		r.passf("constraints: .nojekyll present and zero-byte")
	}
}

// humanSize formats a byte count as a compact human-readable string.
func humanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	suffixes := []string{"KiB", "MiB", "GiB", "TiB"}
	return fmt.Sprintf("%.1f %s", float64(b)/float64(div), suffixes[exp])
}
