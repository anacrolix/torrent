package webtorrent

import (
	"context"
	"net/http"

	"github.com/coder/websocket"
)

// The browser's WebSocket is used on js, which doesn't allow a custom HTTP client or request
// headers.
func dialTracker(ctx context.Context, url string, _ *http.Client, _ http.Header) (*websocket.Conn, error) {
	c, _, err := websocket.Dial(ctx, url, nil)
	return c, err
}
