//go:build !js

package webtorrent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/anacrolix/torrent/tracker"
)

func wsUrl(s *httptest.Server) string {
	return "ws" + strings.TrimPrefix(s.URL, "http")
}

// A tracker that stops reading must not block other uses of the TrackerClient, and writes to it
// must eventually fail.
func TestTrackerClientWriteDoesNotHoldLock(t *testing.T) {
	oldWriteTimeout := writeTimeout
	writeTimeout = 2 * time.Second
	t.Cleanup(func() { writeTimeout = oldWriteTimeout })

	serverDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		// Never read.
		<-serverDone
	}))
	defer server.Close()
	defer close(serverDone)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsUrl(server), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	tc := &TrackerClient{
		GetAnnounceRequest: func(event tracker.AnnounceEvent, _ [20]byte) (tracker.AnnounceRequest, error) {
			return tracker.AnnounceRequest{Event: event}, nil
		},
		OnAnnounceSuccessful: func(string) {},
		OnAnnounceError:      func(string, error) {},
		wsConn:               conn,
		pingTicker:           time.NewTicker(time.Hour),
	}
	tc.cond.L = &tc.mu

	var announces atomic.Int64
	announceErr := make(chan error, 1)
	go func() {
		for {
			if err := tc.announce(tracker.None, [20]byte{}, nil); err != nil {
				announceErr <- err
				return
			}
			announces.Add(1)
		}
	}()
	// Wait for the socket buffers to fill, so a write is blocked.
	for last := int64(-1); ; {
		time.Sleep(100 * time.Millisecond)
		n := announces.Load()
		if n == last {
			break
		}
		last = n
	}

	// Stats takes the lock.
	statsDone := make(chan struct{})
	go func() {
		tc.Stats()
		close(statsDone)
	}()
	select {
	case <-statsDone:
	case <-time.After(time.Second):
		t.Fatal("Stats blocked by a write to the tracker")
	}

	select {
	case err := <-announceErr:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Logf("announce error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("blocked write didn't time out")
	}

	closeDone := make(chan struct{})
	go func() {
		tc.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close blocked")
	}
}

// Tracker messages larger than the read limit end the connection rather than being buffered.
func TestTrackerClientReadLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		conn.Write(r.Context(), websocket.MessageText, make([]byte, 2*maxTrackerMessageSize))
		// Wait for the client to hang up.
		conn.Read(r.Context())
	}))
	defer server.Close()

	tc := &TrackerClient{
		Url:            wsUrl(server),
		OnConnected:    func(error) {},
		OnDisconnected: func(error) {},
		pingTicker:     time.NewTicker(time.Hour),
	}
	defer tc.pingTicker.Stop()
	tc.cond.L = &tc.mu
	done := make(chan error, 1)
	go func() { done <- tc.doWebsocket() }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "read limited") {
			t.Fatalf("expected read limit error, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("oversized message wasn't rejected")
	}
}
