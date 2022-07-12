package webtorrent

import (
	"context"
	"expvar"
	"fmt"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"io"
	"sync"

	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/v2/pproffd"
	"github.com/pion/datachannel"
	"github.com/pion/webrtc/v3"
	"go.opentelemetry.io/otel"
)

var (
	metrics = expvar.NewMap("webtorrent")
	api     = func() *webrtc.API {
		// Enable the detach API (since it's non-standard but more idiomatic).
		s.DetachDataChannels()
		return webrtc.NewAPI(webrtc.WithSettingEngine(s))
	}()
	config              = webrtc.Configuration{ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}}}
	newPeerConnectionMu sync.Mutex
)

type wrappedPeerConnection struct {
	*webrtc.PeerConnection
	closeMu sync.Mutex
	pproffd.CloseWrapper
	span trace.Span
	ctx  context.Context
}

func (me *wrappedPeerConnection) Close() error {
	me.closeMu.Lock()
	defer me.closeMu.Unlock()
	err := me.CloseWrapper.Close()
	me.span.End()
	return err
}

func newPeerConnection(logger log.Logger) (*wrappedPeerConnection, error) {
	newPeerConnectionMu.Lock()
	defer newPeerConnectionMu.Unlock()
	ctx, span := otel.Tracer(tracerName).Start(context.Background(), "PeerConnection")
	pc, err := api.NewPeerConnection(config)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		span.End()
		return nil, err
	}
	wpc := &wrappedPeerConnection{
		PeerConnection: pc,
		CloseWrapper:   pproffd.NewCloseWrapper(pc),
		ctx:            ctx,
		span:           span,
	}
	// If the state change handler intends to call Close, it should call it on the wrapper.
	wpc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		logger.Levelf(log.Warning, "webrtc PeerConnection state changed to %v", state)
		span.AddEvent("connection state changed", trace.WithAttributes(attribute.String("state", state.String())))
	})
	return wpc, nil
}

func setAndGatherLocalDescription(peerConnection *wrappedPeerConnection, sdp webrtc.SessionDescription) (_ webrtc.SessionDescription, err error) {
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection.PeerConnection)
	peerConnection.span.AddEvent("setting local description")
	err = peerConnection.SetLocalDescription(sdp)
	if err != nil {
		err = fmt.Errorf("setting local description: %w", err)
		return
	}
	<-gatherComplete
	peerConnection.span.AddEvent("gathering complete")
	return *peerConnection.LocalDescription(), nil
}

// newOffer creates a transport and returns a WebRTC offer to be announced
func newOffer(
	logger log.Logger,
) (
	peerConnection *wrappedPeerConnection,
	offer webrtc.SessionDescription,
	err error,
) {
	peerConnection, err = newPeerConnection(logger)
	if err != nil {
		return
	}

	peerConnection.span.SetAttributes(attribute.String(webrtcConnTypeKey, "offer"))

	offer, err = peerConnection.CreateOffer(nil)
	if err != nil {
		peerConnection.Close()
		return
	}

	offer, err = setAndGatherLocalDescription(peerConnection, offer)
	if err != nil {
		peerConnection.Close()
	}
	return
}

func initAnsweringPeerConnection(
	peerConnection *wrappedPeerConnection,
	offer webrtc.SessionDescription,
) (answer webrtc.SessionDescription, err error) {
	peerConnection.span.SetAttributes(attribute.String(webrtcConnTypeKey, "answer"))

	err = peerConnection.SetRemoteDescription(offer)
	if err != nil {
		return
	}
	answer, err = peerConnection.CreateAnswer(nil)
	if err != nil {
		return
	}

	answer, err = setAndGatherLocalDescription(peerConnection, answer)
	return
}

// newAnsweringPeerConnection creates a transport from a WebRTC offer and returns a WebRTC answer to be announced.
func newAnsweringPeerConnection(
	logger log.Logger,
	offer webrtc.SessionDescription,
) (
	peerConn *wrappedPeerConnection, answer webrtc.SessionDescription, err error,
) {
	peerConn, err = newPeerConnection(logger)
	if err != nil {
		err = fmt.Errorf("failed to create new connection: %w", err)
		return
	}
	answer, err = initAnsweringPeerConnection(peerConn, offer)
	if err != nil {
		peerConn.span.RecordError(err)
		peerConn.Close()
	}
	return
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
	ctx context.Context,
	dc *webrtc.DataChannel,
	pc *wrappedPeerConnection,
	onOpen func(closer datachannel.ReadWriteCloser),
) {
	dc.OnOpen(func() {
		trace.SpanFromContext(ctx).AddEvent("opened")
		raw, err := dc.Detach()
		if err != nil {
			// This shouldn't happen if the API is configured correctly, and we call from OnOpen.
			panic(err)
		}
		//dc.OnClose()
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
