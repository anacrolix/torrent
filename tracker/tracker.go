package tracker

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/anacrolix/dht/v2/krpc"
	"github.com/anacrolix/torrent/tracker/udp"
)

type AnnounceRequest = udp.AnnounceRequest

type AnnounceResponse struct {
	Interval int32 // Minimum seconds the local peer should wait before next announce.
	Leechers int32
	Seeders  int32
	Peers    []Peer
}

type AnnounceEvent = udp.AnnounceEvent

const (
	None      AnnounceEvent = iota
	Completed               // The local peer just completed the torrent.
	Started                 // The local peer has just resumed this torrent.
	Stopped                 // The local peer is leaving the swarm.
)

var (
	ErrBadScheme = errors.New("unknown scheme")
)

type Announce struct {
	TrackerUrl string
	Request    AnnounceRequest
	HostHeader string
	HTTPProxy  func(*http.Request) (*url.URL, error)
	ServerName string
	UserAgent  string
	UdpNetwork string
	// If the port is zero, it's assumed to be the same as the Request.Port.
	ClientIp4 krpc.NodeAddr
	// If the port is zero, it's assumed to be the same as the Request.Port.
	ClientIp6 krpc.NodeAddr
	Context   context.Context
}

// The code *is* the documentation.
const DefaultTrackerAnnounceTimeout = 15 * time.Second

func (me Announce) Do() (res AnnounceResponse, err error) {
	_url, err := url.Parse(me.TrackerUrl)
	if err != nil {
		return
	}
	if me.Context == nil {
		// This is just to maintain the old behaviour that should be a timeout of 15s. Users can
		// override it by providing their own Context. See comments elsewhere about longer timeouts
		// acting as rate limiting overloaded trackers.
		ctx, cancel := context.WithTimeout(context.Background(), DefaultTrackerAnnounceTimeout)
		defer cancel()
		me.Context = ctx
	}
	switch _url.Scheme {
	case "http", "https":
		return announceHTTP(me, _url)
	case "udp", "udp4", "udp6":
		return announceUDP(me, _url)
	default:
		err = ErrBadScheme
		return
	}
}
