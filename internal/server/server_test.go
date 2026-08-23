package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jcrussell/solvent-streets/pkg/iostreams"
)

func TestReadyURL(t *testing.T) {
	cases := []struct {
		name string
		addr net.Addr
		want string
	}{
		{
			name: "ipv4 loopback",
			addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080},
			want: "http://127.0.0.1:8080",
		},
		{
			name: "ipv4 wildcard becomes localhost",
			addr: &net.TCPAddr{IP: net.ParseIP("0.0.0.0"), Port: 8080},
			want: "http://localhost:8080",
		},
		{
			name: "ipv6 wildcard becomes localhost",
			addr: &net.TCPAddr{IP: net.IPv6unspecified, Port: 9090},
			want: "http://localhost:9090",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := readyURL(tc.addr); got != tc.want {
				t.Errorf("readyURL(%s) = %q, want %q", tc.addr, got, tc.want)
			}
		})
	}
}

// TestNosniffMiddleware pins s68f: every response carries
// X-Content-Type-Options: nosniff.
func TestNosniffMiddleware(t *testing.T) {
	h := nosniffMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q; want %q", got, "nosniff")
	}
}

// TestServer_ReadyFile verifies that ListenAndServe writes the
// listening URL to ReadyFile atomically after bind, before Serve
// begins accepting. Container/test orchestration relies on this to
// avoid sleeping or scraping log lines.
func TestServer_ReadyFile(t *testing.T) {
	// Probe a free port by binding ":0", reading the port, and closing.
	// A race window exists before our server rebinds; acceptable for tests.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	dir := t.TempDir()
	readyPath := filepath.Join(dir, "ready.txt")

	ios, _, _, _ := iostreams.Test()
	srv := New(nil, "127.0.0.1", port, ios)
	srv.ReadyFile = readyPath
	srv.Ready = make(chan struct{})

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe(ctx) }()

	// Wait for the listener to bind via the deterministic Ready channel
	// instead of a poll-and-sleep loop that could expire under CI load.
	select {
	case <-srv.Ready:
	case <-ctx.Done():
		<-errCh
		t.Fatalf("server did not become ready within deadline")
	}
	content, err := os.ReadFile(readyPath)
	if err != nil {
		cancel()
		<-errCh
		t.Fatalf("ready file %q not readable after Ready: %v", readyPath, err)
	}

	got := strings.TrimSpace(string(content))
	wantSuffix := ":" + strconv.Itoa(port)
	if !strings.HasPrefix(got, "http://") || !strings.HasSuffix(got, wantSuffix) {
		t.Errorf("ready file content = %q, want http://...%s", got, wantSuffix)
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("ListenAndServe returned error: %v", err)
	}
}

// TestServer_ReadyFile_Empty confirms an empty ReadyFile suppresses
// the write entirely — the default (no flag) must not create files.
func TestServer_ReadyFile_Empty(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	dir := t.TempDir()
	ios, _, _, _ := iostreams.Test()
	srv := New(nil, "127.0.0.1", port, ios)
	// Leave ReadyFile zero-valued.
	srv.Ready = make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe(ctx) }()

	// Wait for the listener to bind via the deterministic Ready channel
	// (replaces a fixed-duration sleep), then shut it down.
	<-srv.Ready
	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("ListenAndServe returned error: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read tmp dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty dir, found %d entries", len(entries))
	}
}

func TestRecoveryMiddleware_Panic(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})
	handler := recoveryMiddleware(inner, io.Discard)

	req, _ := http.NewRequestWithContext(context.Background(), "GET", "/", nil)
	w := httptest.NewRecorder()

	// Should not panic
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

// TestServer_RejectsOversizedHeaders proves the MaxHeaderBytes cap is real.
// It previously read 1 << 20 -- byte-identical to net/http.DefaultMaxHeaderBytes,
// which is also what http.Server uses when the field is zero -- so the line
// added nothing while the comment beside it claimed hardening that never
// happened. Only a cap strictly below the stdlib default does anything, so this
// asserts a 128 KiB header is refused while an ordinary one is served -- the
// control request is load-bearing, not decoration: it is what stops a transport
// error from a dead server counting as "refused".
func TestServer_RejectsOversizedHeaders(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	ios, _, _, _ := iostreams.Test()
	srv := New(nil, "127.0.0.1", port, ios)
	srv.Ready = make(chan struct{})

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe(ctx) }()
	select {
	case <-srv.Ready:
	case <-ctx.Done():
		<-errCh
		t.Fatalf("server did not become ready within deadline")
	}
	defer func() {
		cancel()
		<-errCh
	}()

	base := "http://127.0.0.1:" + strconv.Itoa(port) + "/"

	do := func(t *testing.T, header string) (*http.Response, error) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base, nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		if header != "" {
			req.Header.Set("X-Bloat", header)
		}
		return http.DefaultClient.Do(req)
	}

	// Control request FIRST. Without it, the oversized case below could accept
	// a transport error from a server that never came up at all -- the test
	// would pass while proving nothing. This establishes that the listener
	// answers ordinary requests, so a later transport error is attributable to
	// the header cap rather than to a dead server.
	resp, err := do(t, "")
	if err != nil {
		t.Fatalf("control request failed; the server is not serving: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("control request got 431; the cap is rejecting ordinary headers")
	}

	// A header well past the 64 KiB cap but well under the 1 MiB stdlib
	// default, so this fails if the cap regresses to a no-op.
	resp, err = do(t, strings.Repeat("a", 128*1024))
	if err != nil {
		// Some stacks drop the connection rather than answering 431. Given the
		// control request succeeded, either outcome means the header was
		// refused, which is the property under test.
		t.Logf("oversized header refused at the transport layer: %v", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Errorf("oversized header: status = %d, want %d (MaxHeaderBytes is a no-op at or above the stdlib default)",
			resp.StatusCode, http.StatusRequestHeaderFieldsTooLarge)
	}
}
