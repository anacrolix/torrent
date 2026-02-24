//go:build !js
// +build !js

package webtorrent

import (
	"testing"

	"github.com/anacrolix/log"
	qt "github.com/go-quicktest/qt"
	"github.com/pion/webrtc/v4"
)

func TestClosingPeerConnectionDoesNotCloseUnopenedDataChannel(t *testing.T) {
	var tc TrackerClient
	pc, dc, _, err := tc.newOffer(log.Default, "", [20]byte{})
	qt.Assert(t, qt.IsNil(err))
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
	qt.Check(t, qt.Equals(dc.ReadyState(), webrtc.DataChannelStateClosed))
	<-peerConnClosed
}
