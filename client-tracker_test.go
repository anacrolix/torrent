package torrent

import (
	"errors"
	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/tracker"
	"github.com/anacrolix/torrent/webtorrent"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClientInvalidTracker(t *testing.T) {
	cfg := TestingConfig(t)
	cfg.DisableTrackers = false
	cfg.Observers = NewClientObservers()

	cl, err := NewClient(cfg)
	require.NoError(t, err)
	defer cl.Close()

	dir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(dir)

	mi.AnnounceList = [][]string{
		{"ws://test.invalid:4242"},
	}

	to, err := cl.AddTorrent(mi)
	require.NoError(t, err)

	status := readChannelTimeout(t, cfg.Observers.Trackers.ConnStatus, 500*time.Millisecond).(webtorrent.TrackerStatus)
	require.Equal(t, "ws://test.invalid:4242", status.Url)
	var expected *net.OpError
	require.ErrorAs(t, expected, &status.Err)

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

	cfg := TestingConfig(t)
	cfg.DisableTrackers = false
	cfg.Observers = NewClientObservers()

	cl, err := NewClient(cfg)
	require.NoError(t, err)
	defer cl.Close()

	dir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(dir)

	mi.AnnounceList = [][]string{
		{trackerUrl},
	}

	to, err := cl.AddTorrent(mi)
	require.NoError(t, err)

	status := readChannelTimeout(t, cfg.Observers.Trackers.ConnStatus, 500*time.Millisecond).(webtorrent.TrackerStatus)
	require.Equal(t, trackerUrl, status.Url)
	require.True(t, status.Ok)
	require.Nil(t, status.Err)

	to.Drop()
}

func TestClientAnnounceFailure(t *testing.T) {
	s, trackerUrl := startTestTracker()
	defer s.Close()

	cfg := TestingConfig(t)
	cfg.DisableTrackers = false
	cfg.Observers = NewClientObservers()

	cl, err := NewClient(cfg)
	require.NoError(t, err)
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
	require.NoError(t, err)

	status := readChannelTimeout(t, cfg.Observers.Trackers.AnnounceStatus, 500*time.Millisecond).(webtorrent.AnnounceStatus)
	require.Equal(t, trackerUrl, status.Url)
	require.False(t, status.Ok)
	require.EqualError(t, status.Err, "test error")
	require.Empty(t, status.Event)

	to.Drop()
}

func TestClientAnnounceSuccess(t *testing.T) {
	s, trackerUrl := startTestTracker()
	defer s.Close()

	cfg := TestingConfig(t)
	cfg.DisableTrackers = false
	cfg.Observers = NewClientObservers()

	cl, err := NewClient(cfg)
	require.NoError(t, err)
	defer cl.Close()

	dir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(dir)

	mi.AnnounceList = [][]string{
		{trackerUrl},
	}

	to, err := cl.AddTorrent(mi)
	require.NoError(t, err)

	status := readChannelTimeout(t, cfg.Observers.Trackers.AnnounceStatus, 500*time.Millisecond).(webtorrent.AnnounceStatus)
	require.Equal(t, trackerUrl, status.Url)
	require.True(t, status.Ok)
	require.Nil(t, status.Err)
	require.Equal(t, "started", status.Event)

	to.Drop()
}

func startTestTracker() (*httptest.Server, string) {
	s := httptest.NewServer(http.HandlerFunc(testtracker))
	trackerUrl := "ws" + strings.TrimPrefix(s.URL, "http")
	return s, trackerUrl
}
