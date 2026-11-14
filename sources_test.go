package torrent

import (
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-quicktest/qt"
)

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
