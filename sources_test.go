package torrent

import (
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/go-quicktest/qt"
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

// The HTTP client transparently decompresses gzip responses, so a small response can decode to
// much more. The body read must be bounded regardless of what the server reports.
func TestMetainfoSourceBodyBounded(t *testing.T) {
	const strLen = 60 << 20
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		gw := gzip.NewWriter(w)
		defer gw.Close()
		gw.Write([]byte("d4:infol"))
		zeros := make([]byte, 1<<20)
		for range 2 {
			gw.Write([]byte(strconv.Itoa(strLen) + ":"))
			for range strLen / len(zeros) {
				if _, err := gw.Write(zeros); err != nil {
					return
				}
			}
		}
		gw.Write([]byte("ee"))
	}))
	defer s.Close()
	_, err := getTorrentSource(context.Background(), s.URL, s.Client())
	qt.Assert(t, qt.IsTrue(errors.Is(err, errMetainfoSourceTooLarge)), qt.Commentf("%v", err))
}

func TestMetainfoSourceContentLengthBounded(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.FormatInt(maxMetainfoSourceSize+1, 10))
		w.WriteHeader(http.StatusOK)
	}))
	defer s.Close()
	_, err := getTorrentSource(context.Background(), s.URL, s.Client())
	qt.Assert(t, qt.IsTrue(errors.Is(err, errMetainfoSourceTooLarge)), qt.Commentf("%v", err))
}
