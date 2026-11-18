package httpTracker

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
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
	// Don't verify the tracker's TLS certificate. Certificates are verified by default.
	InsecureSkipVerify bool
	// Decides whether to follow a redirect, like http.Client.CheckRedirect. If nil,
	// DefaultCheckRedirect is used.
	CheckRedirect func(req *http.Request, via []*http.Request) error
}

// MaxRedirects is the most redirects DefaultCheckRedirect follows for one request.
const MaxRedirects = 5

// DefaultCheckRedirect follows at most MaxRedirects redirects, only to the same host as the
// original request, and not from https to http.
func DefaultCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= MaxRedirects {
		return fmt.Errorf("stopped after %d redirects", len(via))
	}
	first := via[0].URL
	if !strings.EqualFold(req.URL.Hostname(), first.Hostname()) {
		return fmt.Errorf("not following redirect to another host: %q", req.URL.Host)
	}
	if first.Scheme == "https" && req.URL.Scheme != "https" {
		return errors.New("not following redirect from https to http")
	}
	return nil
}

func NewClient(url_ *url.URL, opts NewClientOpts) Client {
	maxResponseBytes := opts.MaxResponseBytes
	if maxResponseBytes == 0 {
		maxResponseBytes = DefaultMaxResponseBytes
	}
	checkRedirect := opts.CheckRedirect
	if checkRedirect == nil {
		checkRedirect = DefaultCheckRedirect
	}
	return Client{
		url_:             url_,
		maxResponseBytes: maxResponseBytes,
		hc: &http.Client{
			CheckRedirect: checkRedirect,
			Transport: &http.Transport{
				DialContext: opts.DialContext,
				Proxy:       opts.Proxy,
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: opts.InsecureSkipVerify,
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
