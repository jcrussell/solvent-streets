package httpio

import (
	"errors"
	"strings"
	"testing"
)

func TestReadAllLimit(t *testing.T) {
	cases := map[string]struct {
		size    int
		max     int64
		wantErr bool
	}{
		"under limit":   {size: 10, max: 100, wantErr: false},
		"exactly limit": {size: 100, max: 100, wantErr: false},
		"one over":      {size: 101, max: 100, wantErr: true},
		"far over":      {size: 10_000, max: 100, wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			body, err := ReadAllLimit(strings.NewReader(strings.Repeat("x", tc.size)), tc.max)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("size %d, max %d: expected error, got nil", tc.size, tc.max)
				}
				if !errors.Is(err, ErrBodyTooLarge) {
					t.Errorf("expected ErrBodyTooLarge, got %v", err)
				}
				// A truncated body must never be returned as success.
				if body != nil {
					t.Errorf("expected nil body on overflow, got %d bytes", len(body))
				}
				return
			}
			if err != nil {
				t.Fatalf("size %d, max %d: unexpected error %v", tc.size, tc.max, err)
			}
			if len(body) != tc.size {
				t.Errorf("expected %d bytes, got %d", tc.size, len(body))
			}
		})
	}
}
