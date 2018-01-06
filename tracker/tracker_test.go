package tracker

import (
	"crypto/tls"
	"net"
	"net/http"
	"testing"
	"time"
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
	_, err := Announce(defaultClient, defaultHTTPUserAgent, "lol://tracker.openbittorrent.com:80/announce", nil)
	if err != ErrBadScheme {
		t.Fatal(err)
	}
}
