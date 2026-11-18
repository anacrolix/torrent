package torrent

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/tracker"
)

// Collects the client's status events on a channel so the test goroutine can wait for a
// particular one. Events can arrive in any order (the websocket tracker announces before it reports
// connecting), and callbacks must not block the tracker client or touch the test's state.
func statusEvents(cfg *ClientConfig) <-chan StatusUpdatedEvent {
	events := make(chan StatusUpdatedEvent, 100)
	cfg.Callbacks.StatusUpdated = append(cfg.Callbacks.StatusUpdated, func(e StatusUpdatedEvent) {
		select {
		case events <- e:
		default:
		}
	})
	return events
}

// Returns the first event of type want, skipping others.
func waitStatusEvent(t *testing.T, events <-chan StatusUpdatedEvent, want StatusEvent) StatusUpdatedEvent {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case e := <-events:
			if e.Event == want {
				return e
			}
		case <-timeout:
			t.Fatalf("no %v status event", want)
		}
	}
}

func TestClientInvalidTracker(t *testing.T) {
	cfg := TestingConfig(t)
	cfg.DisableTrackers = false
	events := statusEvents(cfg)

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

	e := waitStatusEvent(t, events, TrackerDisconnected)
	qt.Check(t, qt.Equals(e.Url, "ws://test.invalid:4242"))
	qt.Check(t, qt.IsNotNil(e.Error))
	to.Drop()
}

func testtracker(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	for {
		_, _, err := c.Read(r.Context())
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

	cfg := TestingConfig(t)
	cfg.DisableTrackers = false
	events := statusEvents(cfg)

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

	e := waitStatusEvent(t, events, TrackerConnected)
	qt.Check(t, qt.Equals(e.Url, trackerUrl))
	qt.Check(t, qt.IsNil(e.Error))
	to.Drop()
}

func TestClientAnnounceFailure(t *testing.T) {
	s, trackerUrl := startTestTracker()
	defer s.Close()

	cfg := TestingConfig(t)
	cfg.DisableTrackers = false
	events := statusEvents(cfg)

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

	to, err := cl.AddTorrent(mi)
	qt.Assert(t, qt.IsNil(err))

	e := waitStatusEvent(t, events, TrackerAnnounceError)
	qt.Check(t, qt.Equals(e.Url, trackerUrl))
	qt.Check(t, qt.Equals(e.InfoHash, to.InfoHash().HexString()))
	qt.Assert(t, qt.IsNotNil(e.Error))
	qt.Check(t, qt.Equals(e.Error.Error(), "test error"))
	to.Drop()
}

func TestClientAnnounceSuccess(t *testing.T) {
	s, trackerUrl := startTestTracker()
	defer s.Close()

	cfg := TestingConfig(t)
	cfg.DisableTrackers = false
	events := statusEvents(cfg)

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

	e := waitStatusEvent(t, events, TrackerAnnounceSuccessful)
	qt.Check(t, qt.Equals(e.Url, trackerUrl))
	qt.Check(t, qt.Equals(e.InfoHash, to.InfoHash().HexString()))
	qt.Check(t, qt.IsNil(e.Error))
	to.Drop()
}

func startTestTracker() (*httptest.Server, string) {
	s := httptest.NewServer(http.HandlerFunc(testtracker))
	trackerUrl := "ws" + strings.TrimPrefix(s.URL, "http")
	return s, trackerUrl
}

// newEventTracker serves HTTP announces, reporting each announce event on the returned channel.
func newEventTracker(t *testing.T) (string, <-chan string) {
	events := make(chan string, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		events <- r.URL.Query().Get("event")
		b, err := bencode.Marshal(map[string]any{"interval": 1800, "peers": ""})
		if err != nil {
			panic(err)
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/announce", events
}

func requireAnnounceEvent(t *testing.T, events <-chan string, want string) {
	t.Helper()
	select {
	case got := <-events:
		qt.Assert(t, qt.Equals(got, want))
	case <-time.After(10 * time.Second):
		t.Fatalf("no %q announce", want)
	}
}

// Dropping a torrent must announce Stopped to its trackers promptly, not when the dispatcher's next
// regular announce is due.
func TestClientDroppedTorrentAnnouncesStopped(t *testing.T) {
	cfg := TestingConfig(t)
	cfg.DisableTrackers = false
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	url, events := newEventTracker(t)
	tor, _ := cl.AddTorrentInfoHash(metainfo.Hash{1, 2, 3})
	tor.AddTrackers([][]string{{url}})
	requireAnnounceEvent(t, events, "started")
	// Stopped is only announced after a successful announce.
	announced := func() bool {
		cl.lock()
		defer cl.unlock()
		for _, state := range tor.regularTrackerAnnounceState {
			if !state.lastOk.Completed.IsZero() {
				return true
			}
		}
		return false
	}
	for deadline := time.Now().Add(10 * time.Second); !announced(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("started announce never completed")
		}
	}
	tor.Drop()
	requireAnnounceEvent(t, events, "stopped")
}

// Each tracker added starts an announce that looks up its tracker client while adding the next one
// writes to the same map. Only -race catches it.
func TestAddTrackersWhileAnnouncing(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("d8:intervali3600e5:peers0:e"))
	}))
	defer s.Close()
	cfg := TestingConfig(t)
	cfg.DisableTrackers = false
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	to, _ := cl.AddTorrentInfoHash(metainfo.Hash{1})
	for i := range 20 {
		to.AddTrackers([][]string{{fmt.Sprintf("%s/%d", s.URL, i)}})
		time.Sleep(time.Millisecond)
	}
}

func TestRemovedWebsocketTrackerIsReleased(t *testing.T) {
	s, trackerURL := startTestTracker()
	defer s.Close()

	cfg := TestingConfig(t)
	cfg.DisableTrackers = false
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()

	dir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(dir)
	mi.AnnounceList = [][]string{{trackerURL}}
	to, err := cl.AddTorrent(mi)
	qt.Assert(t, qt.IsNil(err))
	defer to.Drop()

	cl.websocketTrackers.mu.Lock()
	_, registered := cl.websocketTrackers.clients[trackerURL]
	cl.websocketTrackers.mu.Unlock()
	qt.Assert(t, qt.IsTrue(registered))

	otherHash := [20]byte(to.InfoHash())
	otherHash[0] ^= 0x80
	_, releaseOther := cl.websocketTrackers.Get(trackerURL, otherHash)

	to.ModifyTrackers(nil)
	cl.websocketTrackers.mu.Lock()
	_, stillRegistered := cl.websocketTrackers.clients[trackerURL]
	cl.websocketTrackers.mu.Unlock()
	qt.Check(t, qt.IsTrue(stillRegistered))

	releaseOther()
	cl.websocketTrackers.mu.Lock()
	_, stillRegistered = cl.websocketTrackers.clients[trackerURL]
	cl.websocketTrackers.mu.Unlock()
	qt.Check(t, qt.IsFalse(stillRegistered))
}

func TestRetainedWebsocketTrackerIsNotReleased(t *testing.T) {
	s, trackerURL := startTestTracker()
	defer s.Close()

	cfg := TestingConfig(t)
	cfg.DisableTrackers = false
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()

	dir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(dir)
	mi.AnnounceList = [][]string{{trackerURL}}
	to, err := cl.AddTorrent(mi)
	qt.Assert(t, qt.IsNil(err))
	defer to.Drop()

	cl.lock()
	var key torrentTrackerAnnouncerKey
	var original websocketTrackerStatus
	found := false
	for k, announcer := range to.trackerAnnouncers {
		if status, ok := announcer.(websocketTrackerStatus); ok && status.url.String() == trackerURL {
			key, original, found = k, status, true
			break
		}
	}
	cl.unlock()
	qt.Assert(t, qt.IsTrue(found))

	cl.websocketTrackers.mu.Lock()
	originalClient, registered := cl.websocketTrackers.clients[trackerURL]
	originalRefs := 0
	if registered {
		originalRefs = originalClient.refCount
	}
	cl.websocketTrackers.mu.Unlock()
	qt.Assert(t, qt.IsTrue(registered))

	to.ModifyTrackers([][]string{{trackerURL}})

	cl.lock()
	retained, exists := to.trackerAnnouncers[key]
	cl.unlock()
	qt.Assert(t, qt.IsTrue(exists))
	status, ok := retained.(websocketTrackerStatus)
	qt.Assert(t, qt.IsTrue(ok))
	qt.Check(t, qt.Equals(status.tc, original.tc))

	cl.websocketTrackers.mu.Lock()
	client, registered := cl.websocketTrackers.clients[trackerURL]
	refs := 0
	if registered {
		refs = client.refCount
	}
	cl.websocketTrackers.mu.Unlock()
	qt.Assert(t, qt.IsTrue(registered))
	qt.Check(t, qt.Equals(client, originalClient))
	qt.Check(t, qt.Equals(refs, originalRefs))
}
