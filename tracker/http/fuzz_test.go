package httpTracker

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/anacrolix/torrent/bencode"
)

type fuzzRoundTripper []byte

func (me fuzzRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(me)),
		Request:    req,
	}, nil
}

// Feeds tracker announce responses through the real client.
func FuzzAnnounceResponse(f *testing.F) {
	f.Add([]byte("d8:intervali1800e5:peers6:\x01\x02\x03\x04\x1a\xe1e"))
	f.Add([]byte("d8:completei1e10:incompletei2e8:intervali60e5:peersld2:ip7:1.2.3.44:porti6881eed2:ip3:::14:porti1eeee"))
	f.Add([]byte("d6:peers618:\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01\x1a\xe1e"))
	f.Add([]byte("d14:failure reason4:nopee"))
	u, err := url.Parse("http://tracker.invalid/announce")
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		cl := Client{
			hc:               &http.Client{Transport: fuzzRoundTripper(b)},
			url_:             u,
			maxResponseBytes: DefaultMaxResponseBytes,
		}
		resp, err := cl.Announce(context.Background(), AnnounceRequest{}, AnnounceOpt{})
		if err != nil {
			return
		}
		for _, p := range resp.Peers {
			_ = p.String()
			p.ToNetipAddrPort()
		}
	})
}

func FuzzScrapeResponse(f *testing.F) {
	f.Add([]byte("d5:filesd20:aaaaaaaaaaaaaaaaaaaad8:completei1e10:downloadedi2e10:incompletei3eeee"))
	f.Fuzz(func(t *testing.T, b []byte) {
		var resp scrapeResponse
		_ = bencode.NewDecoder(bytes.NewReader(b)).Decode(&resp)
	})
}
