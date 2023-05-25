package webtorrent

import (
	"context"
	"expvar"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/v2/pproffd"
	"github.com/pion/datachannel"
	"github.com/pion/webrtc/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const (
	dataChannelLabel = "webrtc-datachannel"
)

var (
	metrics = expvar.NewMap("webtorrent")
	api     = func() *webrtc.API {
		// Enable the detach API (since it's non-standard but more idiomatic).
		s.DetachDataChannels()
		return webrtc.NewAPI(webrtc.WithSettingEngine(s))
	}()
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

func newPeerConnection(logger log.Logger, iceServers []string) (*wrappedPeerConnection, error) {
	newPeerConnectionMu.Lock()
	defer newPeerConnectionMu.Unlock()
	ctx, span := otel.Tracer(tracerName).Start(context.Background(), "PeerConnection")

	pcConfig := webrtc.Configuration{ICEServers: []webrtc.ICEServer{{URLs: iceServers}}}

	pc, err := api.NewPeerConnection(pcConfig)
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

// newOffer creates a transport and returns a WebRTC offer to be announced. See
// https://github.com/pion/webrtc/blob/master/examples/data-channels/jsfiddle/main.go for what this is modelled on.
func (tc *TrackerClient) newOffer(
	logger log.Logger,
	offerId string,
	infoHash [20]byte,
) (
	peerConnection *wrappedPeerConnection,
	dataChannel *webrtc.DataChannel,
	offer webrtc.SessionDescription,
	err error,
) {
	peerConnection, err = newPeerConnection(logger, tc.ICEServers)
	if err != nil {
		return
	}

	peerConnection.span.SetAttributes(attribute.String(webrtcConnTypeKey, "offer"))

	dataChannel, err = peerConnection.CreateDataChannel(dataChannelLabel, nil)
	if err != nil {
		err = fmt.Errorf("creating data channel: %w", err)
		peerConnection.Close()
	}
	initDataChannel(dataChannel, peerConnection, func(dc datachannel.ReadWriteCloser, dcCtx context.Context, dcSpan trace.Span) {
		metrics.Add("outbound offers answered with datachannel", 1)
		tc.mu.Lock()
		tc.stats.ConvertedOutboundConns++
		tc.mu.Unlock()
		tc.OnConn(dc, DataChannelContext{
			OfferId:        offerId,
			LocalOffered:   true,
			InfoHash:       infoHash,
			peerConnection: peerConnection,
			Context:        dcCtx,
			Span:           dcSpan,
		})
	})

	offer, err = peerConnection.CreateOffer(nil)
	if err != nil {
		dataChannel.Close()
		peerConnection.Close()
		return
	}

	offer, err = setAndGatherLocalDescription(peerConnection, offer)
	if err != nil {
		dataChannel.Close()
		peerConnection.Close()
	}
	return
}

type onDetachedDataChannelFunc func(detached datachannel.ReadWriteCloser, ctx context.Context, span trace.Span)

func (tc *TrackerClient) initAnsweringPeerConnection(
	peerConn *wrappedPeerConnection,
	offerContext offerContext,
) (answer webrtc.SessionDescription, err error) {
	peerConn.span.SetAttributes(attribute.String(webrtcConnTypeKey, "answer"))

	timer := time.AfterFunc(30*time.Second, func() {
		peerConn.span.SetStatus(codes.Error, "answer timeout")
		metrics.Add("answering peer connections timed out", 1)
		peerConn.Close()
	})
	peerConn.OnDataChannel(func(d *webrtc.DataChannel) {
		initDataChannel(d, peerConn, func(detached datachannel.ReadWriteCloser, ctx context.Context, span trace.Span) {
			timer.Stop()
			metrics.Add("answering peer connection conversions", 1)
			tc.mu.Lock()
			tc.stats.ConvertedInboundConns++
			tc.mu.Unlock()
			tc.OnConn(detached, DataChannelContext{
				OfferId:        offerContext.Id,
				LocalOffered:   false,
				InfoHash:       offerContext.InfoHash,
				peerConnection: peerConn,
				Context:        ctx,
				Span:           span,
			})
		})
	})

	err = peerConn.SetRemoteDescription(offerContext.SessDesc)
	if err != nil {
		return
	}
	answer, err = peerConn.CreateAnswer(nil)
	if err != nil {
		return
	}

	answer, err = setAndGatherLocalDescription(peerConn, answer)
	return
}

// newAnsweringPeerConnection creates a transport from a WebRTC offer and returns a WebRTC answer to be announced.
func (tc *TrackerClient) newAnsweringPeerConnection(
	offerContext offerContext,
) (
	peerConn *wrappedPeerConnection, answer webrtc.SessionDescription, err error,
) {
	peerConn, err = newPeerConnection(tc.Logger, tc.ICEServers)
	if err != nil {
		err = fmt.Errorf("failed to create new connection: %w", err)
		return
	}
	answer, err = tc.initAnsweringPeerConnection(peerConn, offerContext)
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

func initDataChannel(
	dc *webrtc.DataChannel,
	pc *wrappedPeerConnection,
	onOpen onDetachedDataChannelFunc,
) {
	var span trace.Span
	dc.OnClose(func() {
		span.End()
	})
	dc.OnOpen(func() {
		pc.span.AddEvent("data channel opened")
		var ctx context.Context
		ctx, span = otel.Tracer(tracerName).Start(pc.ctx, "DataChannel")
		raw, err := dc.Detach()
		if err != nil {
			// This shouldn't happen if the API is configured correctly, and we call from OnOpen.
			panic(err)
		}
		onOpen(hookDataChannelCloser(raw, pc, span, dc), ctx, span)
	})
}

// Hooks the datachannel's Close to Close the owning PeerConnection. The datachannel takes ownership
// and responsibility for the PeerConnection.
func hookDataChannelCloser(
	dcrwc datachannel.ReadWriteCloser,
	pc *wrappedPeerConnection,
	dataChannelSpan trace.Span,
	originalDataChannel *webrtc.DataChannel,
) datachannel.ReadWriteCloser {
	return struct {
		datachannelReadWriter
		io.Closer
	}{
		dcrwc,
		ioCloserFunc(func() error {
			dcrwc.Close()
			pc.Close()
			originalDataChannel.Close()
			dataChannelSpan.End()
			return nil
		}),
	}
}
