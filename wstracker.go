package torrent

import (
	"context"
	"fmt"
	"net"
	netHttp "net/http"
	"net/url"
	"sync"

	"github.com/anacrolix/log"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"

	"github.com/anacrolix/torrent/tracker"
	httpTracker "github.com/anacrolix/torrent/tracker/http"
	"github.com/anacrolix/torrent/webtorrent"
)

type websocketTrackerStatus struct {
	url url.URL
	tc  *webtorrent.TrackerClient
}

func (me websocketTrackerStatus) statusLine() string {
	return fmt.Sprintf("%+v", me.tc.Stats())
}

func (me websocketTrackerStatus) URL() *url.URL {
	return &me.url
}

func (me websocketTrackerStatus) Stop() {
}

type refCountedWebtorrentTrackerClient struct {
	webtorrent.TrackerClient
	refCount int
}

type websocketTrackers struct {
	PeerId                     [20]byte
	Logger                     log.Logger
	GetAnnounceRequest         func(event tracker.AnnounceEvent, infoHash [20]byte) (tracker.AnnounceRequest, error)
	OnConn                     func(webtorrent.DataChannelConn, webtorrent.DataChannelContext)
	mu                         sync.Mutex
	clients                    map[string]*refCountedWebtorrentTrackerClient
	Proxy                      httpTracker.ProxyFunc
	DialContext                func(ctx context.Context, network, addr string) (net.Conn, error)
	WebsocketTrackerHttpHeader func() netHttp.Header
	ICEServers                 []webrtc.ICEServer
	callbacks                  *Callbacks
}

func (me *websocketTrackers) Get(url string, infoHash [20]byte) (*webtorrent.TrackerClient, func()) {
	me.mu.Lock()
	defer me.mu.Unlock()
	value, ok := me.clients[url]
	if !ok {
		dialer := &websocket.Dialer{
			Proxy:            me.Proxy,
			NetDialContext:   me.DialContext,
			HandshakeTimeout: websocket.DefaultDialer.HandshakeTimeout,
		}
		value = &refCountedWebtorrentTrackerClient{
			TrackerClient: webtorrent.TrackerClient{
				Dialer:             dialer,
				Url:                url,
				GetAnnounceRequest: me.GetAnnounceRequest,
				PeerId:             me.PeerId,
				OnConn:             me.OnConn,
				Logger: me.Logger.WithText(
					func(m log.Msg) string {
						return fmt.Sprintf("tracker client for %q: %v", url, m)
					},
				),
				WebsocketTrackerHttpHeader: me.WebsocketTrackerHttpHeader,
				ICEServers:                 me.ICEServers,
				OnConnected: func(err error) {
					for _, cb := range me.callbacks.StatusUpdated {
						cb(StatusUpdatedEvent{
							Event: TrackerConnected,
							Url:   url,
							Error: err,
						})
					}
				},
				OnDisconnected: func(err error) {
					for _, cb := range me.callbacks.StatusUpdated {
						cb(StatusUpdatedEvent{
							Event: TrackerDisconnected,
							Url:   url,
							Error: err,
						})
					}
				},
				OnAnnounceSuccessful: func(ih string) {
					for _, cb := range me.callbacks.StatusUpdated {
						cb(StatusUpdatedEvent{
							Event:    TrackerAnnounceSuccessful,
							Url:      url,
							InfoHash: ih,
						})
					}
				},
				OnAnnounceError: func(ih string, err error) {
					for _, cb := range me.callbacks.StatusUpdated {
						cb(StatusUpdatedEvent{
							Event:    TrackerAnnounceError,
							Url:      url,
							Error:    err,
							InfoHash: ih,
						})
					}
				},
			},
		}
		value.TrackerClient.Start(func(err error) {
			if err != nil {
				me.Logger.Printf("error running tracker client for %q: %v", url, err)
			}
		})
		if me.clients == nil {
			me.clients = make(map[string]*refCountedWebtorrentTrackerClient)
		}
		me.clients[url] = value
	}
	value.refCount++
	return &value.TrackerClient, func() {
		me.mu.Lock()
		defer me.mu.Unlock()
		value.TrackerClient.CloseOffersForInfohash(infoHash)
		value.refCount--
		if value.refCount == 0 {
			value.TrackerClient.Close()
			delete(me.clients, url)
		}
	}
}
