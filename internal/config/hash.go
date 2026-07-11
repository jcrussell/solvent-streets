package config

import (
	"bytes"
	"crypto/sha256"
	"fmt"

	"github.com/BurntSushi/toml"
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
// don't have raw bytes — they fall back to hashing a TOML re-encoding
// of the struct. TOML is used to mirror parseConfig's own hashing
// primitive (raw TOML bytes) and because the encoder writes struct
// fields in declaration order and dereferences pointers (encoding the
// pointed-to values, not addresses). That determinism matters: a bare
// fmt "%v" renders *ForecastConfig per-city overrides as pointer
// addresses, so two identical in-memory configs would hash differently
// per process. Config has no map fields, so the encoding has no
// ordering ambiguity. The fallback is still unstable across Config
// field additions, but tests rebuild fresh state each run so that's
// fine. The rare "%v" tail below only guards an encoder error (Config
// is always TOML-encodable in practice).
//
// The 16-character truncation matches the existing on-disk format in
// snapshots.config_hash; widening it desynchronizes write vs read.
func (c *Config) Hash() string {
	if c.contentHash != "" {
		return c.contentHash
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return hashBytes(fmt.Appendf(nil, "%v", c))
	}
	return hashBytes(buf.Bytes())
}

func hashBytes(b []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(b))[:16]
}
