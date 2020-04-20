package webtorrent

import (
	"expvar"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/anacrolix/missinggo/v2/pproffd"
	"github.com/pion/datachannel"

	"github.com/pion/webrtc/v2"
)

var (
	metrics = expvar.NewMap("webtorrent")
	api     = func() *webrtc.API {
		// Enable the detach API (since it's non-standard but more idiomatic).
		s := webrtc.SettingEngine{}
		s.DetachDataChannels()
		return webrtc.NewAPI(webrtc.WithSettingEngine(s))
	}()
	config              = webrtc.Configuration{ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}}}
	newPeerConnectionMu sync.Mutex
)

type wrappedPeerConnection struct {
	*webrtc.PeerConnection
	pproffd.CloseWrapper
}

func (me wrappedPeerConnection) Close() error {
	return me.CloseWrapper.Close()
}

func newPeerConnection() (wrappedPeerConnection, error) {
	newPeerConnectionMu.Lock()
	defer newPeerConnectionMu.Unlock()
	pc, err := api.NewPeerConnection(config)
	if err != nil {
		return wrappedPeerConnection{}, err
	}
	return wrappedPeerConnection{
		pc,
		pproffd.NewCloseWrapper(pc),
	}, nil
}

// newOffer creates a transport and returns a WebRTC offer to be announced
func newOffer() (
	peerConnection wrappedPeerConnection,
	dataChannel *webrtc.DataChannel,
	offer webrtc.SessionDescription,
	err error,
) {
	peerConnection, err = newPeerConnection()
	if err != nil {
		return
	}
	dataChannel, err = peerConnection.CreateDataChannel("webrtc-datachannel", nil)
	if err != nil {
		peerConnection.Close()
		return
	}
	offer, err = peerConnection.CreateOffer(nil)
	if err != nil {
		peerConnection.Close()
		return
	}
	err = peerConnection.SetLocalDescription(offer)
	if err != nil {
		peerConnection.Close()
		return
	}
	return
}

func initAnsweringPeerConnection(
	peerConnection wrappedPeerConnection,
	offerId string,
	offer webrtc.SessionDescription,
	onOpen onDataChannelOpen,
) (answer webrtc.SessionDescription, err error) {
	err = peerConnection.SetRemoteDescription(offer)
	if err != nil {
		return
	}
	answer, err = peerConnection.CreateAnswer(nil)
	if err != nil {
		return
	}
	err = peerConnection.SetLocalDescription(answer)
	if err != nil {
		return
	}
	timer := time.AfterFunc(30*time.Second, func() {
		metrics.Add("answering peer connections timed out", 1)
		peerConnection.Close()
	})
	peerConnection.OnDataChannel(func(d *webrtc.DataChannel) {
		setDataChannelOnOpen(d, peerConnection, func(dc datachannel.ReadWriteCloser) {
			timer.Stop()
			metrics.Add("answering peer connection conversions", 1)
			onOpen(dc, DataChannelContext{answer, offer, offerId, false})
		})
	})
	return
}

// getAnswerForOffer creates a transport from a WebRTC offer and and returns a WebRTC answer to be
// announced.
func getAnswerForOffer(
	offer webrtc.SessionDescription, onOpen onDataChannelOpen, offerId string,
) (
	answer webrtc.SessionDescription, err error,
) {
	peerConnection, err := newPeerConnection()
	if err != nil {
		err = fmt.Errorf("failed to peer connection: %w", err)
		return
	}
	answer, err = initAnsweringPeerConnection(peerConnection, offerId, offer, onOpen)
	if err != nil {
		peerConnection.Close()
	}
	return
}

func (t *outboundOffer) setAnswer(answer webrtc.SessionDescription, onOpen func(datachannel.ReadWriteCloser)) error {
	setDataChannelOnOpen(t.dataChannel, t.peerConnection, onOpen)
	err := t.peerConnection.SetRemoteDescription(answer)
	return err
}

type datachannelReadWriter interface {
	datachannel.Reader
	datachannel.Writer
	io.Reader
	io.Writer
}

type ioCloserFunc func() error

func (me ioCloserFunc) Close() error {
	return me()
}

func setDataChannelOnOpen(
	dc *webrtc.DataChannel,
	pc wrappedPeerConnection,
	onOpen func(closer datachannel.ReadWriteCloser),
) {
	dc.OnOpen(func() {
		raw, err := dc.Detach()
		if err != nil {
			// This shouldn't happen if the API is configured correctly, and we call from OnOpen.
			panic(err)
		}
		onOpen(hookDataChannelCloser(raw, pc))
	})
}

func hookDataChannelCloser(dcrwc datachannel.ReadWriteCloser, pc wrappedPeerConnection) datachannel.ReadWriteCloser {
	return struct {
		datachannelReadWriter
		io.Closer
	}{
		dcrwc,
		ioCloserFunc(pc.Close),
	}
}
