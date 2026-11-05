// Package offlinetest keeps a package's tests off the network. Unit tests
// must never reach Overpass, Nominatim, ArcGIS or any other upstream: a live
// call is slow, flaky, rude to a free public service, and in a sandbox with
// no route out it hangs until the go test timeout (solvent-streets-zdv7).
// Network tests belong in integration/, behind PVMT_E2E_NETWORK.
//
// Call Main from TestMain in every package whose code talks to an upstream:
//
//	func TestMain(m *testing.M) { offlinetest.Main(m) }
//
// It swaps http.DefaultTransport for a clone that refuses to dial anything
// but loopback, so httptest servers keep working and a stray live request
// fails at once, naming the host. Every refusal is also recorded and fails
// the package run, because callers often treat a fetch error as soft (the
// ingest water strip warns and carries on) and the test would otherwise pass. Clients built with their own Transport
// (pkg/cmd/factory's) are not covered; tests should inject fetchers or point
// a client at an httptest server instead.
package offlinetest

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
)

var (
	mu      sync.Mutex
	refused []string
)

// Main installs the loopback-only default transport, runs m, and fails the
// run if any test tried to dial an upstream.
func Main(m *testing.M) {
	http.DefaultTransport = Transport()
	code := m.Run()
	if hosts := refusedHosts(); len(hosts) > 0 {
		fmt.Fprintf(os.Stderr, "offlinetest: tests tried to reach the network: %s\n"+
			"Unit tests must not call upstreams; inject a fake fetcher or use an httptest server.\n",
			strings.Join(hosts, ", "))
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func refusedHosts() []string {
	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), refused...)
}

// Transport returns a clone of the stock default transport that dials only
// loopback addresses. Proxy is cleared so the check sees the real target
// host rather than an HTTPS_PROXY from the environment.
func Transport() *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		panic(fmt.Sprintf("offlinetest: http.DefaultTransport is %T, not *http.Transport", http.DefaultTransport))
	}
	t := base.Clone()
	t.Proxy = nil
	var d net.Dialer
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if !IsLoopback(addr) {
			mu.Lock()
			refused = append(refused, addr)
			mu.Unlock()
			return nil, fmt.Errorf("offlinetest: test tried to reach %s; unit tests must not use the network — inject a fake fetcher or use an httptest server", addr)
		}
		return d.DialContext(ctx, network, addr)
	}
	return t
}

// IsLoopback reports whether a host:port dial address is local.
func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
