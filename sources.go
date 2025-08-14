package torrent

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"time"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// Add HTTP endpoints that serve the metainfo. They will be used if the torrent info isn't obtained
// yet. The Client HTTP client is used.
func (t *Torrent) AddSources(sources []string) {
	for _, s := range sources {
		_, loaded := t.activeSources.LoadOrStore(s, nil)
		if loaded {
			continue
		}
		go t.sourcer(s)
	}
}

// Tries fetching metainfo from a (HTTP) source until no longer necessary.
func (t *Torrent) sourcer(source string) {
	var err error
	defer func() {
		panicif.False(t.activeSources.CompareAndSwap(source, nil, err))
	}()
	ctx := t.getInfoCtx
	for {
		var retry g.Option[time.Duration]
		retry, err = t.trySource(source)
		if err == nil || ctx.Err() != nil {
			return
		}
		t.slogger().Warn("error using torrent source", "source", source, "err", err)
		if !retry.Ok {
			return
		}
		select {
		case <-time.After(retry.Unwrap()):
		case <-ctx.Done():
		}
	}
}

// If retry is None, take the error you get as final.
func (t *Torrent) trySource(source string) (retry g.Option[time.Duration], err error) {
	t.sourceMutex.Lock()
	defer t.sourceMutex.Unlock()
	ctx := t.getInfoCtx
	if ctx.Err() != nil {
		return
	}
	var mi metainfo.MetaInfo
	mi, err = getTorrentSource(ctx, source, t.cl.config.MetainfoSourcesClient)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		retry.Set(time.Minute + time.Duration(rand.Int64N(int64(time.Minute))))
		return
	}
	err = t.cl.config.MetainfoSourcesMerger(t, &mi)
	return
}

func getTorrentSource(ctx context.Context, source string, hc *http.Client) (mi metainfo.MetaInfo, err error) {
	var req *http.Request
	if req, err = http.NewRequestWithContext(ctx, http.MethodGet, source, nil); err != nil {
		return
	}
	var resp *http.Response
	if resp, err = hc.Do(req); err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("unexpected response status code: %v", resp.StatusCode)
		return
	}
	err = bencode.NewDecoder(resp.Body).Decode(&mi)
	return
}
