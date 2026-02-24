package main

import (
	"context"
	"log/slog"
)

// Adds an extra level filter check on top of an existing Handler. We can't embed the Handler
// because that gives an escape hatch.
type slogLevelFilterHandler struct {
	minLevel slog.Level
	inner    slog.Handler
}

func (me slogLevelFilterHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= me.minLevel && me.inner.Enabled(ctx, level)
}

func (me slogLevelFilterHandler) Handle(ctx context.Context, record slog.Record) error {
	return me.inner.Handle(ctx, record)
}

func (me slogLevelFilterHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return slogLevelFilterHandler{me.minLevel, me.inner.WithAttrs(attrs)}
}

func (me slogLevelFilterHandler) WithGroup(name string) slog.Handler {
	return slogLevelFilterHandler{me.minLevel, me.inner.WithGroup(name)}
}

var _ slog.Handler = slogLevelFilterHandler{}
