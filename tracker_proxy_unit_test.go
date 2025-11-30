package torrent

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

// Test the specific logic change in getIp method
func TestGetIpProxyLogic(t *testing.T) {
	// Test case 1: HTTP proxy is set - should return dummy IP
	t.Run("HTTP proxy set", func(t *testing.T) {
		cfg := NewDefaultClientConfig()
		proxyURL, err := url.Parse("http://proxy.example.com:8080")
		require.NoError(t, err)
		cfg.HTTPProxy = http.ProxyURL(proxyURL)

		cl := &Client{config: cfg}
		torrent := &Torrent{cl: cl}
		
		scraper := &trackerScraper{
			t: torrent,
		}

		ip, err := scraper.getIp()
		require.NoError(t, err)
		require.Equal(t, net.ParseIP("127.0.0.1"), ip)
	})

	// Test case 2: TrackerDialContext is set - should return dummy IP
	t.Run("TrackerDialContext set", func(t *testing.T) {
		cfg := NewDefaultClientConfig()
		cfg.TrackerDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return nil, nil
		}

		cl := &Client{config: cfg}
		torrent := &Torrent{cl: cl}
		
		scraper := &trackerScraper{
			t: torrent,
		}

		ip, err := scraper.getIp()
		require.NoError(t, err)
		require.Equal(t, net.ParseIP("127.0.0.1"), ip)
	})

	// Test case 3: Both proxy and dial context set - should return dummy IP
	t.Run("Both proxy and dial context set", func(t *testing.T) {
		cfg := NewDefaultClientConfig()
		proxyURL, err := url.Parse("http://proxy.example.com:8080")
		require.NoError(t, err)
		cfg.HTTPProxy = http.ProxyURL(proxyURL)
		cfg.TrackerDialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return nil, nil
		}

		cl := &Client{config: cfg}
		torrent := &Torrent{cl: cl}
		
		scraper := &trackerScraper{
			t: torrent,
		}

		ip, err := scraper.getIp()
		require.NoError(t, err)
		require.Equal(t, net.ParseIP("127.0.0.1"), ip)
	})
}
