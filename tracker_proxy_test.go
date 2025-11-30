package torrent

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTrackerScraperGetIpProxyBypass(t *testing.T) {
	// Test that IP resolution is skipped when HTTP proxy is configured
	cfg := NewDefaultClientConfig()
	proxyURL, err := url.Parse("http://proxy.example.com:8080")
	require.NoError(t, err)
	cfg.HTTPProxy = http.ProxyURL(proxyURL)

	cl, err := NewClient(cfg)
	require.NoError(t, err)
	defer cl.Close()

	torrent := &Torrent{cl: cl}
	trackerURL, err := url.Parse("http://tracker.example.com:8080/announce")
	require.NoError(t, err)

	scraper := &trackerScraper{
		shortInfohash: [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
		u:               *trackerURL,
		t:               torrent,
		stopCh:          make(chan struct{}),
	}

	ip, err := scraper.getIp()
	require.NoError(t, err)
	require.Equal(t, net.ParseIP("127.0.0.1"), ip)
}

func TestTrackerScraperGetIpCustomDialContextBypass(t *testing.T) {
	// Test that IP resolution is skipped when custom dial context is configured
	cfg := NewDefaultClientConfig()
	cfg.TrackerDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, nil
	}

	cl, err := NewClient(cfg)
	require.NoError(t, err)
	defer cl.Close()

	torrent := &Torrent{cl: cl}
	trackerURL, err := url.Parse("http://tracker.example.com:8080/announce")
	require.NoError(t, err)

	scraper := &trackerScraper{
		shortInfohash: [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
		u:               *trackerURL,
		t:               torrent,
		stopCh:          make(chan struct{}),
	}

	ip, err := scraper.getIp()
	require.NoError(t, err)
	require.Equal(t, net.ParseIP("127.0.0.1"), ip)
}
