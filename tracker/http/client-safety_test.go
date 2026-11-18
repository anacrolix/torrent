package httpTracker

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	qt "github.com/go-quicktest/qt"
)

const okAnnounceBody = "d8:intervali1800e5:peers0:e"

func announceTo(t *testing.T, rawUrl string, opts NewClientOpts) error {
	u, err := url.Parse(rawUrl)
	qt.Assert(t, qt.IsNil(err))
	cl := NewClient(u, opts)
	defer cl.Close()
	_, err = cl.Announce(context.Background(), AnnounceRequest{}, AnnounceOpt{})
	return err
}

// Tracker certificates are verified unless InsecureSkipVerify is set.
func TestAnnounceVerifiesCertificate(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(okAnnounceBody))
	}))
	defer s.Close()
	err := announceTo(t, s.URL, NewClientOpts{})
	var certErr *tls.CertificateVerificationError
	qt.Check(t, qt.ErrorAs(err, &certErr))
	qt.Check(t, qt.IsNil(announceTo(t, s.URL, NewClientOpts{InsecureSkipVerify: true})))
}

func TestAnnounceRedirects(t *testing.T) {
	var otherHostHit bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHostHit = true
		w.Write([]byte(okAnnounceBody))
	}))
	defer other.Close()
	// The same server as another host name.
	otherUrl, err := url.Parse(other.URL)
	qt.Assert(t, qt.IsNil(err))
	_, port, err := net.SplitHostPort(otherUrl.Host)
	qt.Assert(t, qt.IsNil(err))
	otherUrl.Host = net.JoinHostPort("localhost", port)

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/announce":
			w.Write([]byte(okAnnounceBody))
		case "/same-host":
			http.Redirect(w, r, "/announce", http.StatusFound)
		case "/other-host":
			http.Redirect(w, r, otherUrl.String()+"/announce", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		}
	}))
	defer s.Close()

	qt.Check(t, qt.IsNil(announceTo(t, s.URL+"/same-host", NewClientOpts{})))

	err = announceTo(t, s.URL+"/other-host", NewClientOpts{})
	qt.Check(t, qt.ErrorMatches(err, `.*not following redirect to another host.*`))
	qt.Check(t, qt.IsFalse(otherHostHit))

	err = announceTo(t, s.URL+"/loop", NewClientOpts{})
	qt.Check(t, qt.ErrorMatches(err, `.*stopped after 5 redirects.*`))

	// Callers can choose their own policy.
	qt.Check(t, qt.IsNil(announceTo(t, s.URL+"/other-host", NewClientOpts{
		CheckRedirect: func(*http.Request, []*http.Request) error { return nil },
	})))
	qt.Check(t, qt.IsTrue(otherHostHit))
}

func TestDefaultCheckRedirectHttpsDowngrade(t *testing.T) {
	via := []*http.Request{{URL: &url.URL{Scheme: "https", Host: "tracker.example"}}}
	err := DefaultCheckRedirect(&http.Request{URL: &url.URL{Scheme: "http", Host: "tracker.example"}}, via)
	qt.Check(t, qt.IsNotNil(err))
	err = DefaultCheckRedirect(&http.Request{URL: &url.URL{Scheme: "https", Host: "TRACKER.example:443"}}, via)
	qt.Check(t, qt.IsNil(err))
}

// Errors include only the start of a bad response body.
func TestAnnounceErrorBodyTruncated(t *testing.T) {
	body := strings.Repeat("\x00", 1<<20)
	for _, status := range []int{http.StatusOK, http.StatusInternalServerError} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			w.Write([]byte(body))
		}))
		err := announceTo(t, s.URL, NewClientOpts{})
		s.Close()
		qt.Assert(t, qt.IsNotNil(err))
		qt.Check(t, qt.IsTrue(len(err.Error()) < 2048), qt.Commentf("status %v: error is %d bytes", status, len(err.Error())))
		qt.Check(t, qt.StringContains(err.Error(), "(1048576 bytes)"))
	}
	// Short bodies are quoted whole.
	qt.Check(t, qt.Equals(quoteBodyForError([]byte("nope")), `"nope"`))
}
