// Package httpio holds shared helpers for reading HTTP response bodies from
// external services safely. It is deliberately dependency-free so both
// internal/ingest and internal/cache can share one size cap and one
// overflow-detecting read, instead of each inlining io.LimitReader (which
// truncates silently) with its own copy of the limit.
package httpio

import (
	"errors"
	"fmt"
	"io"
)

// MaxResponseBodyBytes caps every external API response body (Overpass, ArcGIS,
// Nominatim, and the disk-cache transport). Defends against runaway queries or
// hostile/buggy servers driving the process OOM via an unbounded body. A var
// (not const) so tests can shrink it without round-tripping the full size
// through an httptest.Server.
var MaxResponseBodyBytes int64 = 100 * 1024 * 1024

// ErrBodyTooLarge is returned by ReadAllLimit when a body exceeds the limit.
var ErrBodyTooLarge = errors.New("response body exceeds size limit")

// ReadAllLimit reads r fully but returns ErrBodyTooLarge if it exceeds max,
// rather than silently truncating. io.LimitReader returns a clean EOF at exactly
// max bytes, so a body cut off at the limit is indistinguishable from a complete
// one — the truncated bytes then get cached or fail to parse downstream with an
// opaque error. Reading max+1 bytes detects the overflow. A body of exactly max
// bytes is accepted; anything larger errors. On error the (partial) body is not
// returned, so callers can never cache or parse a truncated read as success.
func ReadAllLimit(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%w (limit %d bytes)", ErrBodyTooLarge, limit)
	}
	return body, nil
}
