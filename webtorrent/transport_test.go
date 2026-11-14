//go:build !js
// +build !js

package webtorrent

import (
	"testing"
	"time"

	"github.com/anacrolix/log"
	qt "github.com/go-quicktest/qt"
	"github.com/pion/webrtc/v4"
)

func TestClosingPeerConnectionDoesNotCloseUnopenedDataChannel(t *testing.T) {
	var tc TrackerClient
	tc.Logger = log.Default
	pc, dc, _, err := tc.newOffer(tc.slogger(), "", [20]byte{})
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

func TestClosedPeerConnectionRemovedFromStats(t *testing.T) {
	var tc TrackerClient
	pc, err := newPeerConnection(tc.slogger(), nil)
	qt.Assert(t, qt.IsNil(err))
	tc.storePeerConnection("offer", pc)
	qt.Assert(t, qt.HasLen(tc.rtcPeerConns, 1))
	qt.Assert(t, qt.IsNil(pc.Close()))

	deadline := time.After(3 * time.Second)
	for {
		tc.mu.Lock()
		count := len(tc.rtcPeerConns)
		tc.mu.Unlock()
		if count == 0 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("closed peer connection retained")
		case <-time.After(time.Millisecond):
		}
	}
}
