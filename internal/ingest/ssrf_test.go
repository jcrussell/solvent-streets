package ingest

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
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
		{"this network 0/8", "0.1.2.3", true},
		{"nat64 local-use", "64:ff9b:1::a00:1", true},
		{"6to4", "2002:a00:1::1", true},
		{"site-local", "fec0::1", true},

		// NAT64 and 6to4 are judged by the IPv4 address they embed, so a
		// DNS64 network reaches public upstreams
		{"nat64 to public", "64:ff9b::808:808", false},
		{"nat64 local-use to public", "64:ff9b:1::808:808", false},
		{"6to4 to public", "2002:808:808::1", false},
		{"nat64 to loopback", "64:ff9b::7f00:1", true},

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
			if tc.blocked && err != nil && strings.Contains(err.Error(), "allow_private_arcgis") {
				t.Errorf("the guard's refusal names the ArcGIS-only override, got: %v", err)
			}
		})
	}
}

// TestPublicDialer_ChecksTheDialedAddress: the dialer refuses the address a
// connection is actually made to, whatever the URL check saw, so a host whose
// DNS answer turns private between the check and the dial (rebinding) is
// still refused. "localhost" stands in for such a host: it resolves at dial
// time to loopback.
func TestPublicDialer_ChecksTheDialedAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	addr := net.JoinHostPort("localhost", port)
	d := &PublicDialer{Dialer: &net.Dialer{Timeout: time.Second}}

	_, err = d.DialContext(context.Background(), "tcp", addr)
	if be, ok := errors.AsType[*BlockedAddressError](err); !ok || be.Host != "localhost" || !be.Addr.IsLoopback() {
		t.Fatalf("dial %s = %v, want a BlockedAddressError for localhost's loopback address", addr, err)
	}
	if c, err := d.DialContext(WithAllowPrivate(context.Background()), "tcp", addr); err != nil {
		t.Errorf("AllowPrivate dial: %v", err)
	} else {
		_ = c.Close()
	}

	// A dial to the proxy the transport chose is not checked: a corporate
	// proxy commonly sits on a private network.
	proxy := d.Proxy(func(*http.Request) (*url.URL, error) { return &url.URL{Scheme: "http", Host: addr}, nil })
	if _, err := proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "public.example"}}); err != nil {
		t.Fatal(err)
	}
	if c, err := d.DialContext(context.Background(), "tcp", addr); err != nil {
		t.Errorf("dial to the configured proxy: %v", err)
	} else {
		_ = c.Close()
	}
}

// TestPublicDialer_CheckDialed covers the resolved-address rule with fake
// dial addresses, for families no test host can listen on.
func TestPublicDialer_CheckDialed(t *testing.T) {
	d := &PublicDialer{}
	for addr, blocked := range map[string]bool{
		"8.8.8.8:443":            false,
		"[2606:4700::1111]:443":  false,
		"10.1.2.3:443":           true,
		"169.254.169.254:80":     true,
		"[::ffff:127.0.0.1]:80":  true,
		"[2002:a00:1::1]:443":    true,
		"[64:ff9b:1::a00:1]:443": true,
		"0.1.2.3:80":             true,
	} {
		err := d.checkDialed("rebind.example", addr)
		if blocked != (err != nil) {
			t.Errorf("checkDialed(%s) = %v, want blocked=%v", addr, err, blocked)
		}
	}
	d.Exempt = netip.Addr.IsLoopback
	if err := d.checkDialed("localhost", "127.0.0.1:80"); err != nil {
		t.Errorf("Exempt loopback: %v", err)
	}
}
