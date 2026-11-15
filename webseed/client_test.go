package webseed

import (
	"context"
	"io"
	"log/slog"
	"testing"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
)

// A request for a URL that doesn't parse fails through its body.
func TestStartNewRequestBadUrl(t *testing.T) {
	info := &metainfo.Info{Name: "a", Length: 10, PieceLength: 16}
	fileIndex := info.FileSegmentsIndex()
	ws := Client{Url: "http://[::1/"}
	ws.SetInfo(info, &fileIndex)
	req := ws.StartNewRequest(context.Background(), RequestSpec{Start: 0, Length: 10}, slog.Default())
	_, err := io.ReadAll(req.Body)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.IsNotNil(context.Cause(req.Context())))
}
