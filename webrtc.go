package torrent

import (
	"io"
	"net"
	"strconv"
	"time"

	"github.com/pion/webrtc/v4"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/anacrolix/torrent/webtorrent"
)

const webrtcNetwork = "webrtc"

type webrtcNetConn struct {
	io.ReadWriteCloser
	webtorrent.DataChannelContext
}

type webrtcNetAddr struct {
	*webrtc.ICECandidate
}

var _ net.Addr = webrtcNetAddr{}

func (webrtcNetAddr) Network() string {
	// Now that we have the ICE candidate, we can tell if it's over udp or tcp. But should we use
	// that for the network?
	return webrtcNetwork
}

func (me webrtcNetAddr) String() string {
	return net.JoinHostPort(me.Address, strconv.FormatUint(uint64(me.Port), 10))
}

func (me webrtcNetConn) LocalAddr() net.Addr {
	// I'm not sure if this evolves over time. It might also be unavailable if the PeerConnection is
	// closed or closes itself. The same concern applies to RemoteAddr.
	pair, err := me.DataChannelContext.GetSelectedIceCandidatePair()
	if err != nil {
		panic(err)
	}
	return webrtcNetAddr{pair.Local}
}

func (me webrtcNetConn) RemoteAddr() net.Addr {
	// See comments on LocalAddr.
	pair, err := me.DataChannelContext.GetSelectedIceCandidatePair()
	if err != nil {
		panic(err)
	}
	return webrtcNetAddr{pair.Remote}
}

// Do we need these for WebRTC connections exposed as net.Conns? Can we set them somewhere inside
// PeerConnection or on the channel or some transport?

func (w webrtcNetConn) SetDeadline(t time.Time) error {
	w.Span.AddEvent("SetDeadline", trace.WithAttributes(attribute.String("time", t.String())))
	return nil
}

func (w webrtcNetConn) SetReadDeadline(t time.Time) error {
	w.Span.AddEvent("SetReadDeadline", trace.WithAttributes(attribute.String("time", t.String())))
	return nil
}

func (w webrtcNetConn) SetWriteDeadline(t time.Time) error {
	w.Span.AddEvent("SetWriteDeadline", trace.WithAttributes(attribute.String("time", t.String())))
	return nil
}

func (w webrtcNetConn) Read(b []byte) (n int, err error) {
	_, span := otel.Tracer(tracerName).Start(w.Context, "Read")
	defer span.End()
	span.SetAttributes(attribute.Int("buf_len", len(b)))
	n, err = w.ReadWriteCloser.Read(b)
	span.RecordError(err)
	span.SetAttributes(attribute.Int("bytes_read", n))
	return
}
