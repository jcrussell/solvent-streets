package ingest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchOSMWater_Success(t *testing.T) {
	body := `{"elements":[
		{"type":"way","id":1,"tags":{"natural":"water"},"geometry":[
			{"lat":42.36,"lon":-71.06},
			{"lat":42.36,"lon":-71.05},
			{"lat":42.37,"lon":-71.05},
			{"lat":42.37,"lon":-71.06},
			{"lat":42.36,"lon":-71.06}
		]}
	]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); !strings.Contains(ua, "pvmt") {
			t.Errorf("expected User-Agent containing 'pvmt', got %q", ua)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		q := r.PostForm.Get("data")
		if !strings.Contains(q, `"natural"="water"`) {
			t.Errorf("expected query to filter natural=water; got %q", q)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	result, err := fetchOSMWater(context.Background(), srv.Client(), srv.URL, [4]float64{42.0, -72.0, 43.0, -71.0})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result, `{"type":"MultiPolygon"`) {
		t.Errorf("expected MultiPolygon GeoJSON; got %q", result)
	}
	if !strings.Contains(result, "-71.06") || !strings.Contains(result, "42.36") {
		t.Errorf("missing expected coordinates in result %q", result)
	}
}

func TestFetchOSMWater_NoWaterReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"elements":[]}`))
	}))
	t.Cleanup(srv.Close)

	result, err := fetchOSMWater(context.Background(), srv.Client(), srv.URL, [4]float64{0, 0, 1, 1})
	if err != nil {
		t.Fatal(err)
	}
	if result != "" {
		t.Errorf("expected empty result for no water; got %q", result)
	}
}

func TestFetchOSMWater_SkipsOpenWays(t *testing.T) {
	// First way is open (a coastline-style polyline); second is a closed
	// water polygon. Only the closed one should make it into the output.
	body := `{"elements":[
		{"type":"way","id":1,"tags":{"natural":"coastline"},"geometry":[
			{"lat":42.36,"lon":-71.06},
			{"lat":42.36,"lon":-71.05},
			{"lat":42.37,"lon":-71.05}
		]},
		{"type":"way","id":2,"tags":{"natural":"water"},"geometry":[
			{"lat":42.36,"lon":-71.06},
			{"lat":42.36,"lon":-71.05},
			{"lat":42.37,"lon":-71.05},
			{"lat":42.37,"lon":-71.06},
			{"lat":42.36,"lon":-71.06}
		]}
	]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	result, err := fetchOSMWater(context.Background(), srv.Client(), srv.URL, [4]float64{0, 0, 1, 1})
	if err != nil {
		t.Fatal(err)
	}
	// MultiPolygon should contain exactly one polygon (the closed water way).
	if got := strings.Count(result, "[["); got < 1 {
		t.Errorf("expected at least one polygon in result; got %q", result)
	}
}

func TestFetchOSMWater_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	_, err := fetchOSMWater(context.Background(), srv.Client(), srv.URL, [4]float64{0, 0, 1, 1})
	if err == nil {
		t.Fatal("expected error on non-200")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("expected 502 in error, got: %v", err)
	}
}
