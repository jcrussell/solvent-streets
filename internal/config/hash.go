package config

import (
	"crypto/sha256"
	"fmt"
)

// Hash returns a fingerprint of the config used to scope
// snapshot-aware reads to the snapshot(s) written by the same config.
// Both the write path (pkg/cmd/compute createSnapshot) and the read
// path (db.Store.WithConfigHash → snapshotQuery) call this so they
// always agree on which snapshot belongs to which config.
//
// Loaded configs hash their raw TOML bytes (set by parseConfig). This
// is:
//   - path-independent — moving the repo doesn't change the hash;
//   - struct-independent — adding fields to Config doesn't invalidate
//     stored hashes for unchanged TOML files;
//   - distinct per example — two different pvmt.toml files always
//     hash differently, which is what fixes the slug-collision case
//     (e.g. Livermore in single-city livermore-ca vs the bay-area-ca
//     metro at different hex_edge_m) — same city slug, different TOML →
//     different hashes → distinct snapshots.
//
// In-memory configs (tests, programmatically-constructed Configs)
// don't have raw bytes — they fall back to hashing the Go struct's
// %v representation. The fallback is unstable across Config field
// additions, but tests rebuild fresh state each run so that's fine.
//
// The 16-character truncation matches the existing on-disk format in
// snapshots.config_hash; widening it desynchronizes write vs read.
func (c *Config) Hash() string {
	if c.contentHash != "" {
		return c.contentHash
	}
	return hashBytes(fmt.Appendf(nil, "%v", c))
}

func hashBytes(b []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(b))[:16]
}
