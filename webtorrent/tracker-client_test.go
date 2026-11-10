//go:build !js

package webtorrent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent/tracker"
	"github.com/coder/websocket"
)

func TestTrackerAnnounceEventEncoding(t *testing.T) {
	for _, test := range []struct {
		name  string
		event tracker.AnnounceEvent
		want  string
	}{
		{"regular", tracker.None, ""},
		{"started", tracker.Started, "started"},
		{"completed", tracker.Completed, "completed"},
		{"stopped", tracker.Stopped, "stopped"},
	} {
		t.Run(test.name, func(t *testing.T) {
			messages := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				_, message, err := conn.Read(r.Context())
				if err == nil {
					messages <- message
				}
			}))
			defer server.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
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
			}
			if err := tc.announce(test.event, [20]byte{}, nil); err != nil {
				t.Fatal(err)
			}

			select {
			case message := <-messages:
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(message, &fields); err != nil {
					t.Fatal(err)
				}
				if got := string(fields["offers"]); got != "[]" {
					t.Fatalf("announce without offers must encode an empty array, got %s", got)
				}
				rawEvent, present := fields["event"]
				if test.event == tracker.None {
					if present {
						t.Fatalf("regular announce must omit event, got %s", rawEvent)
					}
				} else {
					if !present {
						t.Fatalf("missing event %q", test.want)
					}
					var event string
					if err := json.Unmarshal(rawEvent, &event); err != nil {
						t.Fatal(err)
					}
					if event != test.want {
						t.Fatalf("event = %q, want %q", event, test.want)
					}
				}
			case <-ctx.Done():
				t.Fatal("timed out waiting for tracker announce")
			}
		})
	}
}

func TestUnansweredTrackerPingClosesConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		// Deliberately do not read frames, so the client never receives a pong.
		<-r.Context().Done()
	}))
	defer server.Close()

	tc := &TrackerClient{
		Url:           "ws" + strings.TrimPrefix(server.URL, "http"),
		OnConnected:   func(error) {},
		OnDisconnected: func(error) {},
	}
	tc.cond.L = &tc.mu
	tc.pingTicker = time.NewTicker(time.Millisecond)
	defer tc.pingTicker.Stop()

	done := make(chan error, 1)
	go func() {
		done <- tc.doWebsocket()
	}()
	defer func() {
		tc.mu.Lock()
		if tc.wsConn != nil {
			tc.wsConn.CloseNow()
		}
		tc.mu.Unlock()
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected read loop to exit after unanswered ping")
		}
	case <-time.After(pingTimeout + 5*time.Second):
		t.Fatal("tracker read loop did not exit after unanswered ping")
	}
}
