package torrent

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"
	"github.com/gorilla/websocket"

	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/tracker"
)

func TestClientInvalidTracker(t *testing.T) {
	timeout := time.NewTimer(3 * time.Second)
	receivedStatusUpdate := make(chan bool)
	gotTrackerDisconnectedEvt := false
	cfg := TestingConfig(t)
	cfg.DisableTrackers = false
	cfg.Callbacks.StatusUpdated = append(cfg.Callbacks.StatusUpdated, func(e StatusUpdatedEvent) {
		if e.Event == TrackerAnnounceError {
			// ignore
			return
		}
		if e.Event == TrackerDisconnected {
			gotTrackerDisconnectedEvt = true
			qt.Assert(t, qt.Equals(e.Url, "ws://test.invalid:4242"))
			qt.Assert(t, qt.IsNotNil(e.Error))
		}
		receivedStatusUpdate <- true
	})

	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()

	dir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(dir)

	mi.AnnounceList = [][]string{
		{"ws://test.invalid:4242"},
	}

	to, err := cl.AddTorrent(mi)
	qt.Assert(t, qt.IsNil(err))

	select {
	case <-timeout.C:
	case <-receivedStatusUpdate:
	}
	qt.Assert(t, qt.IsTrue(gotTrackerDisconnectedEvt))
	to.Drop()
}

var upgrader = websocket.Upgrader{}

func testtracker(w http.ResponseWriter, r *http.Request) {
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	for {
		_, _, err := c.ReadMessage()
		if err != nil {
			break
		}
		//err = c.WriteMessage(mt, message)
		//if err != nil {
		//	break
		//}
	}
}

func TestClientValidTrackerConn(t *testing.T) {
	s, trackerUrl := startTestTracker()
	defer s.Close()

	timeout := time.NewTimer(3 * time.Second)
	receivedStatusUpdate := make(chan bool)
	gotTrackerConnectedEvt := false
	cfg := TestingConfig(t)
	cfg.DisableTrackers = false
	cfg.Callbacks.StatusUpdated = append(cfg.Callbacks.StatusUpdated, func(e StatusUpdatedEvent) {
		if e.Event == TrackerConnected {
			gotTrackerConnectedEvt = true
			qt.Assert(t, qt.Equals(e.Url, trackerUrl))
			qt.Assert(t, qt.IsNil(e.Error))
		}
		receivedStatusUpdate <- true
	})

	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()

	dir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(dir)

	mi.AnnounceList = [][]string{
		{trackerUrl},
	}

	to, err := cl.AddTorrent(mi)
	qt.Assert(t, qt.IsNil(err))

	select {
	case <-timeout.C:
	case <-receivedStatusUpdate:
	}
	qt.Assert(t, qt.IsTrue(gotTrackerConnectedEvt))
	to.Drop()
}

func TestClientAnnounceFailure(t *testing.T) {
	s, trackerUrl := startTestTracker()
	defer s.Close()

	timeout := time.NewTimer(3 * time.Second)
	receivedStatusUpdate := make(chan bool)
	gotTrackerAnnounceErrorEvt := false
	cfg := TestingConfig(t)
	cfg.DisableTrackers = false

	var to *Torrent

	cfg.Callbacks.StatusUpdated = append(cfg.Callbacks.StatusUpdated, func(e StatusUpdatedEvent) {
		if e.Event == TrackerConnected {
			// ignore
			return
		}
		if e.Event == TrackerAnnounceError {
			gotTrackerAnnounceErrorEvt = true
			qt.Assert(t, qt.Equals(e.Url, trackerUrl))
			qt.Assert(t, qt.Equals(e.InfoHash, to.InfoHash().HexString()))
			qt.Assert(t, qt.IsNotNil(e.Error))
			qt.Assert(t, qt.Equals(e.Error.Error(), "test error"))
		}
		receivedStatusUpdate <- true
	})

	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()

	cl.websocketTrackers.GetAnnounceRequest = func(event tracker.AnnounceEvent, infoHash [20]byte) (tracker.AnnounceRequest, error) {
		return tracker.AnnounceRequest{}, errors.New("test error")
	}

	dir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(dir)

	mi.AnnounceList = [][]string{
		{trackerUrl},
	}

	to, err = cl.AddTorrent(mi)
	qt.Assert(t, qt.IsNil(err))

	select {
	case <-timeout.C:
	case <-receivedStatusUpdate:
	}
	qt.Assert(t, qt.IsTrue(gotTrackerAnnounceErrorEvt))
	to.Drop()
}

func TestClientAnnounceSuccess(t *testing.T) {
	s, trackerUrl := startTestTracker()
	defer s.Close()

	timeout := time.NewTimer(3 * time.Second)
	receivedStatusUpdate := make(chan bool)
	gotTrackerAnnounceSuccessfulEvt := false
	cfg := TestingConfig(t)
	cfg.DisableTrackers = false

	var to *Torrent

	cfg.Callbacks.StatusUpdated = append(cfg.Callbacks.StatusUpdated, func(e StatusUpdatedEvent) {
		if e.Event == TrackerConnected {
			// ignore
			return
		}
		if e.Event == TrackerAnnounceSuccessful {
			gotTrackerAnnounceSuccessfulEvt = true
			qt.Assert(t, qt.Equals(e.Url, trackerUrl))
			qt.Assert(t, qt.Equals(e.InfoHash, to.InfoHash().HexString()))
			qt.Assert(t, qt.IsNil(e.Error))
		}
		receivedStatusUpdate <- true
	})

	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()

	dir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(dir)

	mi.AnnounceList = [][]string{
		{trackerUrl},
	}

	to, err = cl.AddTorrent(mi)
	qt.Assert(t, qt.IsNil(err))

	select {
	case <-timeout.C:
	case <-receivedStatusUpdate:
	}
	qt.Assert(t, qt.IsTrue(gotTrackerAnnounceSuccessfulEvt))
	to.Drop()
}

func startTestTracker() (*httptest.Server, string) {
	s := httptest.NewServer(http.HandlerFunc(testtracker))
	trackerUrl := "ws" + strings.TrimPrefix(s.URL, "http")
	return s, trackerUrl
}
