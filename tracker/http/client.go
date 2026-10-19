package httpTracker

import (
	"context"
	"crypto/tls"
	"errors"
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

// Returns a reader for the response body that fails if it exceeds the client's limit.
func (cl Client) limitBody(body io.Reader) io.Reader {
	if cl.maxResponseBytes < 0 {
		return body
	}
	return &limitedReader{r: body, n: cl.maxResponseBytes}
}

// Like io.LimitedReader, but returns an error rather than EOF if the limit is exceeded.
type limitedReader struct {
	r io.Reader
	n int64
}

func (me *limitedReader) Read(b []byte) (n int, err error) {
	if me.n <= 0 {
		// Probe for any data past the limit.
		var probe [1]byte
		n, err = me.r.Read(probe[:])
		if n > 0 {
			err = ErrResponseTooLarge
		}
		return 0, err
	}
	if int64(len(b)) > me.n {
		b = b[:me.n]
	}
	n, err = me.r.Read(b)
	me.n -= int64(n)
	return
}

var ErrResponseTooLarge = errors.New("tracker response too large")

func (cl Client) Close() error {
	cl.hc.CloseIdleConnections()
	return nil
}
