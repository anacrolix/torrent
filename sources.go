package torrent

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	if resp.ContentLength > maxMetainfoSourceSize {
		err = fmt.Errorf("response content length %v: %w", resp.ContentLength, errMetainfoSourceTooLarge)
		return
	}
	// The body may be transparently decompressed, so the content length alone isn't enough.
	r := &maxSizeReader{r: resp.Body, n: maxMetainfoSourceSize}
	d := bencode.NewDecoder(r)
	d.MaxStrLen = maxMetainfoSourceSize
	err = d.Decode(&mi)
	if r.exceeded {
		err = fmt.Errorf("%w: %w", errMetainfoSourceTooLarge, err)
	}
	return
}

// The most metainfo bytes read from a source. Info is limited to maxMetadataSize, and v2 piece
// layers can be about as large again.
const maxMetainfoSourceSize = 4 * int64(maxMetadataSize)

var errMetainfoSourceTooLarge = errors.New("metainfo source response too large")

// Like io.LimitReader, but returns errMetainfoSourceTooLarge instead of EOF at the limit.
type maxSizeReader struct {
	r        io.Reader
	n        int64
	exceeded bool
}

func (me *maxSizeReader) Read(b []byte) (n int, err error) {
	if me.n <= 0 {
		me.exceeded = true
		return 0, errMetainfoSourceTooLarge
	}
	if int64(len(b)) > me.n {
		b = b[:me.n]
	}
	n, err = me.r.Read(b)
	me.n -= int64(n)
	return
}
