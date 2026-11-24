//go:build !js
// +build !js

package webtorrent

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/anacrolix/log"
	qt "github.com/go-quicktest/qt"
	"github.com/pion/datachannel"
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

type testMessageReader struct {
	datachannel.ReadWriteCloser
	messages [][]byte
}

func (r *testMessageReader) Read(p []byte) (int, error) {
	if len(r.messages) == 0 {
		return 0, io.EOF
	}
	if len(p) < len(r.messages[0]) {
		return 0, io.ErrShortBuffer
	}
	n := copy(p, r.messages[0])
	r.messages = r.messages[1:]
	return n, nil
}

func TestDataChannelConnReassemblesMessageReads(t *testing.T) {
	handshake := append([]byte{19}, []byte("BitTorrent protocol")...)
	handshake = append(handshake, bytes.Repeat([]byte{0}, 48)...)
	if len(handshake) != 68 {
		t.Fatalf("invalid handshake fixture length %d", len(handshake))
	}
	payload := bytes.Repeat([]byte{0xa5}, 16*1024+13)
	raw := &testMessageReader{messages: [][]byte{handshake, payload}}
	conn := wrapDataChannel(raw, nil, nil, nil)

	gotHandshake := make([]byte, 68)
	if _, err := io.ReadFull(conn, gotHandshake[:1]); err != nil {
		t.Fatalf("reading BitTorrent handshake length: %v", err)
	}
	if _, err := io.ReadFull(conn, gotHandshake[1:]); err != nil {
		t.Fatalf("reading remainder of BitTorrent handshake: %v", err)
	}
	if !bytes.Equal(gotHandshake, handshake) {
		t.Fatal("handshake content changed at DataChannel message boundary")
	}

	gotPayload := make([]byte, len(payload))
	for i := 0; i < len(gotPayload); {
		end := min(i+4096, len(gotPayload))
		if _, err := io.ReadFull(conn, gotPayload[i:end]); err != nil {
			t.Fatalf("reading piece payload at %d: %v", i, err)
		}
		i = end
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Fatal("piece content changed across multiple small reads")
	}
	var extra [1]byte
	if n, err := conn.Read(extra[:]); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after all messages, got n=%d err=%v", n, err)
	}
}
