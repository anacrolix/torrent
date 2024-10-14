package webtorrent

import (
	"testing"

	"github.com/anacrolix/log"
	qt "github.com/frankban/quicktest"
	"github.com/pion/webrtc/v4"
)

func TestClosingPeerConnectionDoesNotCloseUnopenedDataChannel(t *testing.T) {
	c := qt.New(t)
	var tc TrackerClient
	pc, dc, _, err := tc.newOffer(log.Default, "", [20]byte{})
	c.Assert(err, qt.IsNil)
	defer pc.Close()
	defer dc.Close()
	peerConnClosed := make(chan struct{})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateClosed {
			close(peerConnClosed)
		}
	})
	dc.OnClose(func() {
		// This should not be called because the DataChannel is never opened.
		t.Fatal("DataChannel.OnClose handler called")
	})
	t.Logf("data channel ready state before close: %v", dc.ReadyState())
	dc.OnError(func(err error) {
		t.Logf("data channel error: %v", err)
	})
	pc.Close()
	c.Check(dc.ReadyState(), qt.Equals, webrtc.DataChannelStateClosed)
	<-peerConnClosed
}
