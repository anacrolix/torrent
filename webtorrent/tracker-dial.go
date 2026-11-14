//go:build !js

package webtorrent

import (
	"context"
	"net/http"

	"github.com/coder/websocket"
)

func dialTracker(ctx context.Context, url string, httpClient *http.Client, header http.Header) (*websocket.Conn, error) {
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPClient: httpClient,
		HTTPHeader: header,
	})
	return c, err
}
