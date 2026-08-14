package ingest

import (
	"net/netip"
	"strings"
	"testing"
)

// TestCheckPublicAddr pins the SSRF address policy (1nws): the stdlib
// private/loopback/link-local predicates PLUS the special-use prefix blocklist,
// with IPv4-mapped IPv6 collapsed via Unmap first so a mapped private/loopback
// address cannot slip through.
func TestCheckPublicAddr(t *testing.T) {
	cases := []struct {
		name    string
		addr    string
		blocked bool
	}{
		// stdlib predicates
		{"loopback v4", "127.0.0.1", true},
		{"private 10/8", "10.0.0.1", true},
		{"link-local imds", "169.254.169.254", true},
		{"unspecified", "0.0.0.0", true},

		// IPv4-mapped IPv6 must be caught after Unmap
		{"mapped private", "::ffff:10.0.0.1", true},
		{"mapped loopback", "::ffff:127.0.0.1", true},

		// special-use prefix blocklist
		{"cgnat 100.64/10", "100.64.0.1", true},
		{"cgnat high", "100.127.255.254", true},
		{"test-net-1", "192.0.2.5", true},
		{"test-net-2", "198.51.100.5", true},
		{"test-net-3", "203.0.113.5", true},
		{"benchmarking 198.18/15", "198.19.0.1", true},
		{"reserved 240/4", "240.0.0.1", true},
		{"broadcast", "255.255.255.255", true},
		{"nat64 well-known", "64:ff9b::1", true},

		// public addresses must pass
		{"public v4", "8.8.8.8", false},
		{"public v4 near cgnat", "100.63.255.255", false}, // just below 100.64.0.0/10
		{"public v6", "2606:4700:4700::1111", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr, err := netip.ParseAddr(tc.addr)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.addr, err)
			}
			err = checkPublicAddr(addr, "host.example")
			if tc.blocked && err == nil {
				t.Errorf("expected %s to be blocked, got nil", tc.addr)
			}
			if !tc.blocked && err != nil {
				t.Errorf("expected %s to be allowed, got: %v", tc.addr, err)
			}
			if tc.blocked && err != nil && !strings.Contains(err.Error(), "allow_private_arcgis") {
				t.Errorf("blocked error should mention the override flag, got: %v", err)
			}
		})
	}
}
