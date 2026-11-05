package offlinetest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTransport_AllowsLoopbackRefusesUpstream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	client := &http.Client{Transport: Transport()}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("loopback request refused: %v", err)
	}
	_ = resp.Body.Close()

	req, _ = http.NewRequestWithContext(context.Background(), http.MethodGet, "https://overpass-api.de/api/interpreter", nil)
	resp, err = client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("request to overpass-api.de succeeded; want it refused")
	}
	if !strings.Contains(err.Error(), "overpass-api.de:443") {
		t.Errorf("error %q should name the host it refused", err)
	}
}

func TestIsLoopback(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8080":                  true,
		"[::1]:443":                       true,
		"localhost:80":                    true,
		"overpass-api.de:443":             false,
		"nominatim.openstreetmap.org:443": false,
		"10.0.0.1:80":                     false,
	} {
		if got := IsLoopback(addr); got != want {
			t.Errorf("IsLoopback(%q) = %v, want %v", addr, got, want)
		}
	}
}
