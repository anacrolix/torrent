package httpTracker

import (
	"context"
	"log"
	"net/http"
	"net/url"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/tracker/udp"
	"github.com/anacrolix/torrent/types/infohash"
)

type scrapeResponse struct {
	Files files `bencode:"files"`
}

// Bencode should support bencode.Unmarshalers from a string in the dict key position.
type files = map[string]udp.ScrapeInfohashResult

func (cl Client) Scrape(ctx context.Context, ihs []infohash.T) (out udp.ScrapeResponse, err error) {
	_url := cl.url_.JoinPath("..", "scrape")
	query, err := url.ParseQuery(_url.RawQuery)
	if err != nil {
		return
	}
	for _, ih := range ihs {
		query.Add("info_hash", ih.AsString())
	}
	_url.RawQuery = query.Encode()
	log.Printf("%q", _url.String())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, _url.String(), nil)
	if err != nil {
		return
	}
	resp, err := cl.hc.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var decodedResp scrapeResponse
	err = bencode.NewDecoder(resp.Body).Decode(&decodedResp)
	for _, ih := range ihs {
		out = append(out, decodedResp.Files[ih.AsString()])
	}
	return
}
