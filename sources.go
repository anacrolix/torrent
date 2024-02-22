package torrent

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/anacrolix/log"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// Add HTTP endpoints that serve the metainfo. They will be used if the torrent info isn't obtained
// yet. The Client HTTP client is used.
func (t *Torrent) UseSources(sources []string) {
	select {
	case <-t.Closed():
		return
	case <-t.GotInfo():
		return
	default:
	}
	for _, s := range sources {
		_, loaded := t.activeSources.LoadOrStore(s, struct{}{})
		if loaded {
			continue
		}
		s := s
		go func() {
			err := t.useActiveTorrentSource(s)
			_, loaded := t.activeSources.LoadAndDelete(s)
			if !loaded {
				panic(s)
			}
			level := log.Debug
			if err != nil && !errors.Is(err, context.Canceled) {
				level = log.Info
			}
			t.logger.Levelf(level, "used torrent source %q [err=%v]", s, err)
		}()
	}
}

func (t *Torrent) useActiveTorrentSource(source string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-t.GotInfo():
		case <-t.Closed():
		case <-ctx.Done():
		}
		cancel()
	}()
	mi, err := getTorrentSource(ctx, source, t.cl.httpClient)
	if err != nil {
		return err
	}
	return t.MergeSpec(TorrentSpecFromMetaInfo(&mi))
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
