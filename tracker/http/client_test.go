package httpTracker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	qt "github.com/go-quicktest/qt"
)

// A valid announce response padded with an unused key to n bytes of padding.
func announceBody(n int) string {
	return fmt.Sprintf("d8:intervali1800e5:peers0:4:zpad%d:%se", n, strings.Repeat("x", n))
}

func TestAnnounceResponseSizeLimit(t *testing.T) {
	body := announceBody(100)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer s.Close()
	u, err := url.Parse(s.URL)
	qt.Assert(t, qt.IsNil(err))
	announce := func(max int64) error {
		cl := NewClient(u, NewClientOpts{MaxResponseBytes: max})
		defer cl.Close()
		_, err := cl.Announce(context.Background(), AnnounceRequest{}, AnnounceOpt{})
		return err
	}
	qt.Check(t, qt.IsNil(announce(0)))
	qt.Check(t, qt.IsNil(announce(-1)))
	qt.Check(t, qt.IsNil(announce(int64(len(body)))))
	err = announce(int64(len(body) - 1))
	var mbe *http.MaxBytesError
	qt.Check(t, qt.ErrorAs(err, &mbe))
}
