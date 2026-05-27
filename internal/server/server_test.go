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

func TestCORSMiddleware_Options(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inner handler should not be called for OPTIONS")
	})
	handler := corsMiddleware(inner)

	req, _ := http.NewRequestWithContext(context.Background(), "OPTIONS", "/data/meta.json", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("expected CORS Allow-Origin header")
	}
	if w.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Error("expected CORS Allow-Methods header")
	}
}

func TestCORSMiddleware_GET(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := corsMiddleware(inner)

	req, _ := http.NewRequestWithContext(context.Background(), "GET", "/data/meta.json", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("expected CORS Allow-Origin header on GET response")
	}
}

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
	srv := New(nil, port, ios)
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
	srv := New(nil, port, ios)
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
