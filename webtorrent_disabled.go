//go:build nowebtorrent

package torrent

import "net/url"

// The nowebtorrent build tag leaves out WebRTC peers and the ws:// and wss://
// trackers that find them, which drops pion and its dependencies from the
// binary. Web seeds and all other peer sources are unaffected.

// ICEServer describes a STUN/TURN server for WebRTC peer connections. It is
// unused with the nowebtorrent build tag.
type ICEServer struct {
	URLs       []string
	Username   string
	Credential any
}

type websocketTrackers struct{}

func (cl *Client) initWebsocketTrackers() {}

func (t *Torrent) startWebsocketAnnouncer(url.URL, [20]byte) torrentTrackerAnnouncer {
	return nil
}

type webRtcStatsReports map[string]any

func (t *Torrent) GetWebRtcPeerConnStats() map[string]webRtcStatsReports {
	return nil
}
