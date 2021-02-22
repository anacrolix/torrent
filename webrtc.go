package torrent

import (
	"net"
	"time"

	"github.com/pion/datachannel"
	"github.com/pion/webrtc/v3"

	"github.com/anacrolix/torrent/webtorrent"
)

const webrtcNetwork = "webrtc"

type webrtcNetConn struct {
	datachannel.ReadWriteCloser
	webtorrent.DataChannelContext
}

type webrtcNetAddr struct {
	webrtc.SessionDescription
}

func (webrtcNetAddr) Network() string {
	return webrtcNetwork
}

func (me webrtcNetAddr) String() string {
	// TODO: What can I show here that's more like other protocols?
	return "<WebRTC>"
}

func (me webrtcNetConn) LocalAddr() net.Addr {
	return webrtcNetAddr{me.Local}
}

func (me webrtcNetConn) RemoteAddr() net.Addr {
	return webrtcNetAddr{me.Remote}
}

func (w webrtcNetConn) SetDeadline(t time.Time) error {
	return nil
}

func (w webrtcNetConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (w webrtcNetConn) SetWriteDeadline(t time.Time) error {
	return nil
}
