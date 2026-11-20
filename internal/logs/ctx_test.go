package logs

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestFromReturnsDefaultWhenAbsent(t *testing.T) {
	if got := From(context.Background()); got != slog.Default() {
		t.Errorf("From(empty ctx) = %p, want slog.Default() %p", got, slog.Default())
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
	if got := From(ctx); got != slog.Default() {
		t.Errorf("From after WithLogger(nil) = %p, want slog.Default() %p", got, slog.Default())
	}
}
