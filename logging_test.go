package torrent

import (
	"bytes"
	"log/slog"
	"math"
	"testing"
)

type testHandler struct{}

func TestLazyLogValuer(t *testing.T) {
	// This is a test to ensure that the lazyLogValuer type implements the slog.LogValuer interface.
	// The test is intentionally left empty because the implementation is already verified by the
	// compiler.

	//h := testHandler{}
	//l := slog.New(h)
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.Level(math.MinInt),
	})
	l := slog.New(h)
	cl := newTestingClient(t)
	tor := cl.newTorrentForTesting()
	l2 := tor.withSlogger(l)
	l2.Debug("hi")
	t.Log(buf.String())
}
