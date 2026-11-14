package httpTracker

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/url"
)

type Client struct {
	hc               *http.Client
	url_             *url.URL
	maxResponseBytes int64
}

// DefaultMaxResponseBytes is the default limit on the size of a tracker response body.
const DefaultMaxResponseBytes = 4 << 20

type (
	ProxyFunc       func(*http.Request) (*url.URL, error)
	DialContextFunc func(ctx context.Context, network, addr string) (net.Conn, error)
)

type NewClientOpts struct {
	Proxy          ProxyFunc
	DialContext    DialContextFunc
	ServerName     string
	AllowKeepAlive bool
	// Maximum size of a tracker response body. Zero means DefaultMaxResponseBytes. Negative means
	// no limit.
	MaxResponseBytes int64
}

func NewClient(url_ *url.URL, opts NewClientOpts) Client {
	maxResponseBytes := opts.MaxResponseBytes
	if maxResponseBytes == 0 {
		maxResponseBytes = DefaultMaxResponseBytes
	}
	return Client{
		url_:             url_,
		maxResponseBytes: maxResponseBytes,
		hc: &http.Client{
			Transport: &http.Transport{
				DialContext: opts.DialContext,
				Proxy:       opts.Proxy,
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
					ServerName:         opts.ServerName,
				},
				// This is for S3 trackers that hold connections open.
				DisableKeepAlives: !opts.AllowKeepAlive,
			},
		},
	}
}

// Returns a reader for the response body that fails with *http.MaxBytesError if it exceeds the
// client's limit.
func (cl Client) limitBody(body io.ReadCloser) io.Reader {
	if cl.maxResponseBytes < 0 {
		return body
	}
	// The ResponseWriter is only used to close server connections, so nil is fine for clients.
	return http.MaxBytesReader(nil, body, cl.maxResponseBytes)
}

func (cl Client) Close() error {
	cl.hc.CloseIdleConnections()
	return nil
}
