package torrent

import (
	"log/slog"
)

type lazyLogValuer func() any

func (me lazyLogValuer) LogValue() slog.Value {
	return slog.AnyValue(me())
}

var _ slog.LogValuer = lazyLogValuer(nil)
