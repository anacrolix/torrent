package webtorrent

import (
	"fmt"
	"log"
	"sync"

	"github.com/pion/datachannel"

	"github.com/pion/webrtc/v2"
)

var (
	api = func() *webrtc.API {
		// Enable the detach API (since it's non-standard but more idiomatic)
		// (This should be done once globally)
		s := webrtc.SettingEngine{}
		s.DetachDataChannels()
		return webrtc.NewAPI(webrtc.WithSettingEngine(s))
	}()
	config              = webrtc.Configuration{ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}}}
	newPeerConnectionMu sync.Mutex
)

func newPeerConnection() (*webrtc.PeerConnection, error) {
	newPeerConnectionMu.Lock()
	defer newPeerConnectionMu.Unlock()
	return api.NewPeerConnection(config)
}

type Transport struct {
	pc *webrtc.PeerConnection
	dc *webrtc.DataChannel

	lock sync.Mutex
}

// NewTransport creates a transport and returns a WebRTC offer to be announced
func NewTransport() (*Transport, webrtc.SessionDescription, error) {
	peerConnection, err := newPeerConnection()
	if err != nil {
		return nil, webrtc.SessionDescription{}, fmt.Errorf("failed to peer connection: %v\n", err)
	}
	dataChannel, err := peerConnection.CreateDataChannel("webrtc-datachannel", nil)
	if err != nil {
		return nil, webrtc.SessionDescription{}, fmt.Errorf("failed to data channel: %v\n", err)
	}
	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		fmt.Printf("ICE Connection State has changed: %s\n", connectionState.String())
	})

	dataChannel.OnMessage(func(msg webrtc.DataChannelMessage) {
		fmt.Printf("Message from DataChannel '%s': '%s'\n", dataChannel.Label(), string(msg.Data))
	})
	offer, err := peerConnection.CreateOffer(nil)
	if err != nil {
		return nil, webrtc.SessionDescription{}, fmt.Errorf("failed to create offer: %v\n", err)
	}
	err = peerConnection.SetLocalDescription(offer)
	if err != nil {
		return nil, webrtc.SessionDescription{}, fmt.Errorf("failed to set local description: %v\n", err)
	}

	t := &Transport{pc: peerConnection, dc: dataChannel}
	return t, offer, nil
}

// NewTransportFromOffer creates a transport from a WebRTC offer and and returns a WebRTC answer to
// be announced.
func NewTransportFromOffer(offer webrtc.SessionDescription, onOpen onDataChannelOpen) (*Transport, webrtc.SessionDescription, error) {
	peerConnection, err := newPeerConnection()
	if err != nil {
		return nil, webrtc.SessionDescription{}, fmt.Errorf("failed to peer connection: %v", err)
	}
	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		fmt.Printf("ICE Connection State has changed: %s\n", connectionState.String())
	})

	t := &Transport{pc: peerConnection}

	err = peerConnection.SetRemoteDescription(offer)
	if err != nil {
		return nil, webrtc.SessionDescription{}, fmt.Errorf("%v", err)
	}
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		return nil, webrtc.SessionDescription{}, fmt.Errorf("%v", err)
	}
	peerConnection.OnDataChannel(func(d *webrtc.DataChannel) {
		fmt.Printf("New DataChannel %s %d\n", d.Label(), d.ID())
		t.lock.Lock()
		t.dc = d
		t.lock.Unlock()
		t.handleOpen(func(dc datachannel.ReadWriteCloser) {
			onOpen(dc, DataChannelContext{answer, offer, false})
		})
	})
	err = peerConnection.SetLocalDescription(answer)
	if err != nil {
		return nil, webrtc.SessionDescription{}, fmt.Errorf("%v", err)
	}

	return t, answer, nil
}

// SetAnswer sets the WebRTC answer
func (t *Transport) SetAnswer(answer webrtc.SessionDescription, onOpen func(datachannel.ReadWriteCloser)) error {
	t.handleOpen(onOpen)

	err := t.pc.SetRemoteDescription(answer)
	if err != nil {
		return err
	}
	return nil
}

func (t *Transport) handleOpen(onOpen func(datachannel.ReadWriteCloser)) {
	t.lock.Lock()
	dc := t.dc
	t.lock.Unlock()
	dc.OnOpen(func() {
		fmt.Printf("Data channel '%s'-'%d' open.\n", dc.Label(), dc.ID())

		// Detach the data channel
		raw, err := dc.Detach()
		if err != nil {
			log.Fatalf("failed to detach: %v", err) // TODO: Error handling
		}

		onOpen(raw)
	})
}
