package torrent

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// The default config sets a source fetch timeout.
func TestMetainfoSourceTimeoutDefault(t *testing.T) {
	qt.Assert(t, qt.IsTrue(NewDefaultClientConfig().MetainfoSourcesTimeout > 0))
}

// A source that accepts the connection and then never finishes the response must not hold the fetch
// open forever. With the per-attempt timeout the client gives up on the request, which the server
// observes as a cancelled request context. Without it the sourcer blocks until the torrent is
// dropped. This models a magnet whose xs/as source points at a server that stalls.
func TestMetainfoSourceTimesOut(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-r.Context().Done():
			select {
			case cancelled <- struct{}{}:
			default:
			}
		case <-time.After(30 * time.Second):
		}
	}))
	defer srv.Close()

	cfg := TestingConfig(t)
	cfg.MetainfoSourcesTimeout = 300 * time.Millisecond
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()

	// A torrent with no info keeps its getInfoCtx active, so the only bound on the fetch is the
	// per-attempt timeout.
	tt, _ := cl.AddTorrentOpt(AddTorrentOpts{InfoHash: metainfo.HashBytes([]byte("stall"))})
	tt.AddSources([]string{srv.URL})

	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("source fetch was not bounded by the timeout; the attempt hung")
	}
}

// A source that responds promptly with valid metainfo is still fetched and applied with the timeout
// active, so the timeout doesn't break normal operation.
func TestMetainfoSourceSucceedsWithinTimeout(t *testing.T) {
	info := metainfo.Info{Name: "x", PieceLength: 16384, Length: 5, Pieces: make([]byte, 20)}
	infoBytes, err := bencode.Marshal(info)
	qt.Assert(t, qt.IsNil(err))
	miBytes, err := bencode.Marshal(metainfo.MetaInfo{InfoBytes: infoBytes})
	qt.Assert(t, qt.IsNil(err))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(miBytes)
	}))
	defer srv.Close()

	cfg := TestingConfig(t)
	cfg.MetainfoSourcesTimeout = 5 * time.Second
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()

	tt, _ := cl.AddTorrentOpt(AddTorrentOpts{InfoHash: metainfo.HashBytes(infoBytes)})
	tt.AddSources([]string{srv.URL})

	select {
	case <-tt.GotInfo():
	case <-time.After(5 * time.Second):
		t.Fatal("metainfo from a valid source was not applied")
	}
}
