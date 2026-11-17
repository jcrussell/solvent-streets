package db

import (
	"fmt"
	"time"
)

// Every timestamp column (*_at in 001_init.sql) stores INTEGER unix seconds,
// UTC. These are the only conversions: every bind goes through unixSeconds
// and every scan through unixTime, so stored values compare as numbers (gc's
// `computed_at < ?`) and read back as UTC at second precision, the form the
// CLI and JSON print.

// unixSeconds is t as stored: unix seconds.
func unixSeconds(t time.Time) int64 { return t.Unix() }

// fromUnix is a stored timestamp as a UTC time.
func fromUnix(s int64) time.Time { return time.Unix(s, 0).UTC() }

// nowUnix is the current time as stored.
func nowUnix() int64 { return unixSeconds(time.Now()) }

// unixTime scans a stored timestamp into *dst. NULL (a LEFT JOIN with no
// match, or a nullable column) leaves the zero time.
type unixTime struct{ dst *time.Time }

func (u unixTime) Scan(src any) error {
	switch v := src.(type) {
	case int64:
		*u.dst = fromUnix(v)
	case nil:
		*u.dst = time.Time{}
	default:
		return fmt.Errorf("timestamp: stored as %T, want INTEGER unix seconds", src)
	}
	return nil
}
