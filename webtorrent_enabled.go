//go:build !nowebtorrent

package torrent

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/v2"
	"github.com/pion/webrtc/v4"

	"github.com/anacrolix/torrent/tracker"
	"github.com/anacrolix/torrent/webtorrent"
)

// ICEServer describes a STUN/TURN server for WebRTC peer connections.
type ICEServer = webrtc.ICEServer

func (cl *Client) initWebsocketTrackers() {
	cl.websocketTrackers = websocketTrackers{
		PeerId:  cl.peerID,
		Slogger: cl.slogger.With("name", "websocketTrackers"),
		GetAnnounceRequest: func(
			event tracker.AnnounceEvent, infoHash [20]byte,
		) (
			tracker.AnnounceRequest, error,
		) {
			cl.lock()
			defer cl.unlock()
			t, ok := cl.torrentsByShortHash[infoHash]
			if !ok {
				return tracker.AnnounceRequest{}, errors.New("torrent not tracked by client")
			}
			return t.announceRequest(event, infoHash), nil
		},
		Proxy:                      cl.config.HTTPProxy,
		WebsocketTrackerHttpHeader: cl.config.WebsocketTrackerHttpHeader,
		ICEServers:                 cl.ICEServers(),
		DialContext:                cl.config.TrackerDialContext,
		callbacks:                  &cl.config.Callbacks,
		OnConn: func(dc webtorrent.DataChannelConn, dcc webtorrent.DataChannelContext) {
			cl.lock()
			defer cl.unlock()
			t, ok := cl.torrentsByShortHash[dcc.InfoHash]
			if !ok {
				cl.logger.WithDefaultLevel(log.Warning).Printf(
					"got webrtc conn for unloaded torrent with infohash %x",
					dcc.InfoHash,
				)
				dc.Close()
				return
			}
			go t.onWebRtcConn(dc, dcc)
		},
	}
}

func (cl *Client) ICEServers() []webrtc.ICEServer {
	var ICEServers []webrtc.ICEServer
	if cl.config.ICEServerList != nil {
		ICEServers = cl.config.ICEServerList
	} else if cl.config.ICEServers != nil {
		ICEServers = []webrtc.ICEServer{{URLs: cl.config.ICEServers}}
	}
	return ICEServers
}

func (t *Torrent) onWebRtcConn(
	c webtorrent.DataChannelConn,
	dcc webtorrent.DataChannelContext,
) {
	defer c.Close()
	netConn := webrtcNetConn{
		ReadWriteCloser:    c,
		DataChannelContext: dcc,
	}
	peerRemoteAddr := netConn.RemoteAddr()
	//t.logger.Levelf(log.Critical, "onWebRtcConn remote addr: %v", peerRemoteAddr)
	if t.cl.badPeerAddr(peerRemoteAddr) {
		return
	}
	localAddrIpPort := missinggo.IpPortFromNetAddr(netConn.LocalAddr())

	pc, err := t.cl.initiateProtocolHandshakes(
		context.Background(),
		netConn,
		t,
		false,
		newConnectionOpts{
			outgoing:        dcc.LocalOffered,
			remoteAddr:      peerRemoteAddr,
			localPublicAddr: localAddrIpPort,
			network:         webrtcNetwork,
			connString:      fmt.Sprintf("webrtc offer_id %x: %v", dcc.OfferId, regularNetConnPeerConnConnString(netConn)),
		},
	)
	if err != nil {
		t.logger.WithDefaultLevel(log.Error).Printf("error in handshaking webrtc connection: %v", err)
		return
	}
	if dcc.LocalOffered {
		pc.Discovery = PeerSourceTracker
	} else {
		pc.Discovery = PeerSourceIncoming
	}
	pc.conn.SetWriteDeadline(time.Time{})
	t.cl.lock()
	defer t.cl.unlock()
	err = t.runHandshookConn(pc)
	if err != nil {
		t.logger.WithDefaultLevel(log.Debug).Printf("error running handshook webrtc conn: %v", err)
	}
}

func (t *Torrent) startWebsocketAnnouncer(u url.URL, shortInfohash [20]byte) torrentTrackerAnnouncer {
	wtc, release := t.cl.websocketTrackers.Get(u.String(), shortInfohash)
	// This needs to run before the Torrent is dropped from the Client, to prevent a new
	// webtorrent.TrackerClient for the same info hash before the old one is cleaned up.
	t.onClose = append(t.onClose, release)
	wst := websocketTrackerStatus{u, wtc}
	go func() {
		err := wtc.Announce(tracker.Started, shortInfohash)
		if err != nil {
			level := log.Warning
			if t.closed.IsSet() {
				level = log.Debug
			}
			t.logger.Levelf(level, "error doing initial announce to %q: %v", u.String(), err)
		}
	}()
	return wst
}

type webRtcStatsReports map[string]webrtc.StatsReport

func (t *Torrent) GetWebRtcPeerConnStats() map[string]webRtcStatsReports {
	stats := make(map[string]webRtcStatsReports)
	trackersMap := t.cl.websocketTrackers.clients
	for i, trackerClient := range trackersMap {
		ts := trackerClient.RtcPeerConnStats()
		stats[i] = ts
	}
	return stats
}
