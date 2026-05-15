// Package logs holds the context-key helpers for the per-invocation
// slog.Logger. The root command's PersistentPreRunE attaches the run's
// logger to the leaf command's context via WithLogger; command bodies
// pull it back out with From. Falls back to slog.Default when nothing
// has been attached, so package-level slog.Info calls still work.
package logs

import (
	"context"
	"log/slog"
)

type ctxKey struct{}

// WithLogger returns a copy of ctx that carries l. Use in PersistentPreRunE
// to attach a per-run logger (typically logger.With("cmd", path)).
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	if l == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, l)
}

// From returns the logger attached to ctx, or slog.Default if none is.
func From(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
