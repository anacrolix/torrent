package tracker

import (
	"crypto/tls"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var defaultClient = &http.Client{
	Timeout: time.Second * 15,
	Transport: &http.Transport{
		Dial: (&net.Dialer{
			Timeout: 15 * time.Second,
		}).Dial,
		TLSHandshakeTimeout: 15 * time.Second,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
	},
}

func TestUnsupportedTrackerScheme(t *testing.T) {
	t.Parallel()
	_, err := Announce{TrackerUrl: "lol://tracker.openbittorrent.com:80/announce"}.Do()
	require.Equal(t, ErrBadScheme, err)
}
