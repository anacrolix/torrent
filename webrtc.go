package torrent

import (
	"net"
	"time"

	"github.com/pion/datachannel"
)

type webrtcNetConn struct {
	datachannel.ReadWriteCloser
}

type webrtcNetAddr struct {
}

func (webrtcNetAddr) Network() string {
	return "webrtc"
}

func (webrtcNetAddr) String() string {
	return ""
}

func (w webrtcNetConn) LocalAddr() net.Addr {
	return webrtcNetAddr{}
}

func (w webrtcNetConn) RemoteAddr() net.Addr {
	return webrtcNetAddr{}
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
