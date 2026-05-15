package logs

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestFromReturnsDefaultWhenAbsent(t *testing.T) {
	got := From(context.Background())
	if got == nil {
		t.Fatal("From returned nil; expected slog.Default")
	}
}

func TestWithLoggerRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, nil))
	ctx := WithLogger(context.Background(), l)
	From(ctx).Info("hello", "k", "v")
	if !strings.Contains(buf.String(), "hello") || !strings.Contains(buf.String(), "k=v") {
		t.Errorf("attached logger not used; buf=%q", buf.String())
	}
}

func TestWithLoggerNilIsNoop(t *testing.T) {
	ctx := WithLogger(context.Background(), nil)
	if got := From(ctx); got == nil {
		t.Fatal("From returned nil after WithLogger(nil)")
	}
}
