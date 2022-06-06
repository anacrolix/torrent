package webtorrent

import (
	"expvar"
	"fmt"
	"io"
	"sync"

	"github.com/anacrolix/missinggo/v2/pproffd"
	"github.com/pion/datachannel"
	"github.com/pion/webrtc/v3"
)

var (
	metrics = expvar.NewMap("webtorrent")
	config  = webrtc.Configuration{ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}}}
)

func init() {
	// Enable the detach API (since it's non-standard but more idiomatic).
	s.DetachDataChannels()
}

func NewWebRtcSettingsEngine() webrtc.SettingEngine {
	return s
}

type wrappedPeerConnection struct {
	*webrtc.PeerConnection
	closeMu sync.Mutex
	pproffd.CloseWrapper
}

func (me *wrappedPeerConnection) Close() error {
	me.closeMu.Lock()
	defer me.closeMu.Unlock()
	return me.CloseWrapper.Close()
}

func newPeerConnection(api *webrtc.API) (*wrappedPeerConnection, error) {
	pc, err := api.NewPeerConnection(config)
	if err != nil {
		return nil, err
	}
	return &wrappedPeerConnection{
		PeerConnection: pc,
		CloseWrapper:   pproffd.NewCloseWrapper(pc),
	}, nil
}

// newOffer creates a transport and returns a WebRTC offer to be announced
func newOffer(
	api *webrtc.API,
) (
	peerConnection *wrappedPeerConnection,
	dataChannel *webrtc.DataChannel,
	offer webrtc.SessionDescription,
	err error,
) {
	peerConnection, err = newPeerConnection(api)
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

	gatherComplete := webrtc.GatheringCompletePromise(peerConnection.PeerConnection)
	err = peerConnection.SetLocalDescription(offer)
	if err != nil {
		peerConnection.Close()
		return
	}
	<-gatherComplete

	offer = *peerConnection.LocalDescription()
	return
}

func initAnsweringPeerConnection(
	peerConnection *wrappedPeerConnection,
	offer webrtc.SessionDescription,
) (answer webrtc.SessionDescription, err error) {
	err = peerConnection.SetRemoteDescription(offer)
	if err != nil {
		return
	}
	answer, err = peerConnection.CreateAnswer(nil)
	if err != nil {
		return
	}

	gatherComplete := webrtc.GatheringCompletePromise(peerConnection.PeerConnection)
	err = peerConnection.SetLocalDescription(answer)
	if err != nil {
		return
	}
	<-gatherComplete

	answer = *peerConnection.LocalDescription()
	return
}

// newAnsweringPeerConnection creates a transport from a WebRTC offer and returns a WebRTC answer to
// be announced.
func newAnsweringPeerConnection(
	offer webrtc.SessionDescription,
	apis []*webrtc.API,
) (
	peerConn *wrappedPeerConnection,
	answer webrtc.SessionDescription,
	ok bool,
	errs []error,
) {
	for _, api := range apis {
		var err error
		peerConn, err = newPeerConnection(api)
		if err != nil {
			err = fmt.Errorf("new peer connection from %v: %w", api, err)
			errs = append(errs, err)
			continue
		}
		answer, err = initAnsweringPeerConnection(peerConn, offer)
		if err != nil {
			peerConn.Close()
			err = fmt.Errorf("answering %v with %v: %w", offer, peerConn, err)
			errs = append(errs, err)
			continue
		}
		ok = true
		return
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
	pc *wrappedPeerConnection,
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

// Hooks the datachannel's Close to Close the owning PeerConnection. The datachannel takes ownership
// and responsibility for the PeerConnection.
func hookDataChannelCloser(dcrwc datachannel.ReadWriteCloser, pc *wrappedPeerConnection) datachannel.ReadWriteCloser {
	return struct {
		datachannelReadWriter
		io.Closer
	}{
		dcrwc,
		ioCloserFunc(pc.Close),
	}
}
