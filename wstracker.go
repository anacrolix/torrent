package torrent

import (
	"fmt"
	"net/url"
	"sync"

	"github.com/anacrolix/log"

	"github.com/anacrolix/torrent/tracker"
	"github.com/anacrolix/torrent/webtorrent"
	"github.com/pion/datachannel"
)

type websocketTracker struct {
	url url.URL
	*webtorrent.TrackerClient
}

func (me websocketTracker) statusLine() string {
	return fmt.Sprintf("%q", me.url.String())
}

func (me websocketTracker) URL() url.URL {
	return me.url
}

type refCountedWebtorrentTrackerClient struct {
	webtorrent.TrackerClient
	refCount int
}

type websocketTrackers struct {
	PeerId             [20]byte
	Logger             log.Logger
	GetAnnounceRequest func(event tracker.AnnounceEvent, infoHash [20]byte) tracker.AnnounceRequest
	OnConn             func(datachannel.ReadWriteCloser, webtorrent.DataChannelContext)
	mu                 sync.Mutex
	clients            map[string]*refCountedWebtorrentTrackerClient
}

func (me *websocketTrackers) Get(url string) (*webtorrent.TrackerClient, func()) {
	me.mu.Lock()
	defer me.mu.Unlock()
	value, ok := me.clients[url]
	if !ok {
		value = &refCountedWebtorrentTrackerClient{
			TrackerClient: webtorrent.TrackerClient{
				Url:                url,
				GetAnnounceRequest: me.GetAnnounceRequest,
				PeerId:             me.PeerId,
				OnConn:             me.OnConn,
				Logger: me.Logger.WithText(func(m log.Msg) string {
					return fmt.Sprintf("tracker client for %q: %v", url, m)
				}),
			},
		}
		go func() {
			err := value.TrackerClient.Run()
			if err != nil {
				me.Logger.Printf("error running tracker client for %q: %v", url, err)
			}
		}()
		if me.clients == nil {
			me.clients = make(map[string]*refCountedWebtorrentTrackerClient)
		}
		me.clients[url] = value
	}
	value.refCount++
	return &value.TrackerClient, func() {
		me.mu.Lock()
		defer me.mu.Unlock()
		value.refCount--
		if value.refCount == 0 {
			value.TrackerClient.Close()
			delete(me.clients, url)
		}
	}
}
