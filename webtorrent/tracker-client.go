package webtorrent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/log"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v4"
	"go.opentelemetry.io/otel/trace"

	"github.com/anacrolix/torrent/tracker"
	"github.com/anacrolix/torrent/types/infohash"
)

type TrackerClientStats struct {
	Dials                  int64
	ConvertedInboundConns  int64
	ConvertedOutboundConns int64
}

// Client represents the webtorrent client
type TrackerClient struct {
	Url                string
	GetAnnounceRequest func(_ tracker.AnnounceEvent, infoHash [20]byte) (tracker.AnnounceRequest, error)
	PeerId             [20]byte
	OnConn             onDataChannelOpen
	Logger             log.Logger
	Dialer             *websocket.Dialer

	mu             sync.Mutex
	cond           sync.Cond
	outboundOffers map[string]outboundOfferValue // OfferID to outboundOfferValue
	wsConn         *websocket.Conn
	closed         bool
	stats          TrackerClientStats
	pingTicker     *time.Ticker

	WebsocketTrackerHttpHeader func() http.Header
	ICEServers                 []webrtc.ICEServer

	rtcPeerConns map[string]*wrappedPeerConnection

	// callbacks
	OnConnected          func(error)
	OnDisconnected       func(error)
	OnAnnounceSuccessful func(ih string)
	OnAnnounceError      func(ih string, err error)
}

func (me *TrackerClient) Stats() TrackerClientStats {
	me.mu.Lock()
	defer me.mu.Unlock()
	return me.stats
}

func (me *TrackerClient) peerIdBinary() string {
	return binaryToJsonString(me.PeerId[:])
}

type outboundOffer struct {
	offerId string
	outboundOfferValue
}

// outboundOfferValue represents an outstanding offer.
type outboundOfferValue struct {
	originalOffer  webrtc.SessionDescription
	peerConnection *wrappedPeerConnection
	infoHash       [20]byte
	dataChannel    *webrtc.DataChannel
}

type DataChannelContext struct {
	OfferId      string
	LocalOffered bool
	InfoHash     [20]byte
	// This is private as some methods might not be appropriate with data channel context.
	peerConnection *wrappedPeerConnection
	Span           trace.Span
	Context        context.Context
}

func (me *DataChannelContext) GetSelectedIceCandidatePair() (*webrtc.ICECandidatePair, error) {
	return me.peerConnection.SCTP().Transport().ICETransport().GetSelectedCandidatePair()
}

type onDataChannelOpen func(_ DataChannelConn, dcc DataChannelContext)

func (tc *TrackerClient) doWebsocket() error {
	metrics.Add("websocket dials", 1)
	tc.mu.Lock()
	tc.stats.Dials++
	tc.mu.Unlock()

	var header http.Header
	if tc.WebsocketTrackerHttpHeader != nil {
		header = tc.WebsocketTrackerHttpHeader()
	}

	c, _, err := tc.Dialer.Dial(tc.Url, header)
	if err != nil {
		tc.OnDisconnected(err)
		return fmt.Errorf("dialing tracker: %w", err)
	}
	defer c.Close()
	tc.Logger.WithDefaultLevel(log.Info).Printf("connected")
	tc.mu.Lock()
	tc.wsConn = c
	tc.cond.Broadcast()
	tc.mu.Unlock()
	tc.announceOffers()
	closeChan := make(chan struct{})
	go func() {
		for {
			select {
			case <-tc.pingTicker.C:
				tc.mu.Lock()
				err := c.WriteMessage(websocket.PingMessage, []byte{})
				tc.mu.Unlock()
				if err != nil {
					return
				}
			case <-closeChan:
				return

			}
		}
	}()
	tc.OnConnected(nil)
	err = tc.trackerReadLoop(tc.wsConn)
	close(closeChan)
	tc.mu.Lock()
	c.Close()
	tc.mu.Unlock()
	return err
}

// Finishes initialization and spawns the run routine, calling onStop when it completes with the
// result. We don't let the caller just spawn the runner directly, since then we can race against
// .Close to finish initialization.
func (tc *TrackerClient) Start(onStop func(error)) {
	tc.pingTicker = time.NewTicker(60 * time.Second)
	tc.cond.L = &tc.mu
	go func() {
		onStop(tc.run())
	}()
}

func (tc *TrackerClient) run() error {
	tc.mu.Lock()
	for !tc.closed {
		tc.mu.Unlock()
		err := tc.doWebsocket()
		level := log.Info
		tc.mu.Lock()
		if tc.closed {
			level = log.Debug
		}
		tc.mu.Unlock()
		tc.Logger.WithDefaultLevel(level).Printf("websocket instance ended: %v", err)
		time.Sleep(time.Minute)
		tc.mu.Lock()
	}
	tc.mu.Unlock()
	return nil
}

func (tc *TrackerClient) Close() error {
	tc.mu.Lock()
	tc.closed = true
	if tc.wsConn != nil {
		tc.wsConn.Close()
	}
	tc.closeUnusedOffers()
	tc.pingTicker.Stop()
	tc.mu.Unlock()
	tc.cond.Broadcast()
	return nil
}

func (tc *TrackerClient) announceOffers() {
	// tc.Announce grabs a lock on tc.outboundOffers. It also handles the case where outboundOffers
	// is nil. Take ownership of outboundOffers here.
	tc.mu.Lock()
	offers := tc.outboundOffers
	tc.outboundOffers = nil
	tc.mu.Unlock()

	if offers == nil {
		return
	}

	// Iterate over our locally-owned offers, close any existing "invalid" ones from before the
	// socket reconnected, reannounce the infohash, adding it back into the tc.outboundOffers.
	tc.Logger.WithDefaultLevel(log.Info).Printf("reannouncing %d infohashes after restart", len(offers))
	for _, offer := range offers {
		// TODO: Capture the errors? Are we even in a position to do anything with them?
		offer.peerConnection.Close()
		// Use goroutine here to allow read loop to start and ensure the buffer drains.
		go tc.Announce(tracker.Started, offer.infoHash)
	}
}

func (tc *TrackerClient) closeUnusedOffers() {
	for _, offer := range tc.outboundOffers {
		offer.peerConnection.Close()
		offer.dataChannel.Close()
	}
	tc.outboundOffers = nil
}

func (tc *TrackerClient) CloseOffersForInfohash(infoHash [20]byte) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	for key, offer := range tc.outboundOffers {
		if offer.infoHash == infoHash {
			offer.peerConnection.Close()
			delete(tc.outboundOffers, key)
		}
	}
}

func (tc *TrackerClient) Announce(event tracker.AnnounceEvent, infoHash [20]byte) error {
	metrics.Add("outbound announces", 1)
	if event == tracker.Stopped {
		return tc.announce(event, infoHash, nil)
	}
	var randOfferId [20]byte
	_, err := rand.Read(randOfferId[:])
	if err != nil {
		return fmt.Errorf("generating offer_id bytes: %w", err)
	}
	offerIDBinary := binaryToJsonString(randOfferId[:])

	pc, dc, offer, err := tc.newOffer(tc.Logger, offerIDBinary, infoHash)
	if err != nil {
		return fmt.Errorf("creating offer: %w", err)
	}

	// save the leecher peer connections
	tc.storePeerConnection(fmt.Sprintf("%x", randOfferId[:]), pc)

	pc.OnClose(func() {
		delete(tc.rtcPeerConns, offerIDBinary)
	})

	tc.Logger.Levelf(log.Debug, "announcing offer")
	err = tc.announce(event, infoHash, []outboundOffer{{
		offerId: offerIDBinary,
		outboundOfferValue: outboundOfferValue{
			originalOffer:  offer,
			peerConnection: pc,
			infoHash:       infoHash,
			dataChannel:    dc,
		}},
	})
	if err != nil {
		dc.Close()
		pc.Close()
	}
	return err
}

func (tc *TrackerClient) announce(event tracker.AnnounceEvent, infoHash [20]byte, offers []outboundOffer) error {
	request, err := tc.GetAnnounceRequest(event, infoHash)
	if err != nil {
		tc.OnAnnounceError(infohash.T(infoHash).HexString(), err)
		return fmt.Errorf("getting announce parameters: %w", err)
	}

	req := AnnounceRequest{
		Numwant:    len(offers),
		Uploaded:   request.Uploaded,
		Downloaded: request.Downloaded,
		Left:       request.Left,
		Event:      request.Event.String(),
		Action:     "announce",
		InfoHash:   binaryToJsonString(infoHash[:]),
		PeerID:     tc.peerIdBinary(),
	}
	for _, offer := range offers {
		req.Offers = append(req.Offers, Offer{
			OfferID: offer.offerId,
			Offer:   offer.originalOffer,
		})
	}

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshalling request: %w", err)
	}

	tc.mu.Lock()
	defer tc.mu.Unlock()
	err = tc.writeMessage(data)
	if err != nil {
		tc.OnAnnounceError(infohash.T(infoHash).HexString(), err)
		return fmt.Errorf("write AnnounceRequest: %w", err)
	}
	tc.OnAnnounceSuccessful(infohash.T(infoHash).HexString())
	g.MakeMapIfNil(&tc.outboundOffers)
	for _, offer := range offers {
		g.MapInsert(tc.outboundOffers, offer.offerId, offer.outboundOfferValue)
	}
	return nil
}

// Calculate the stats for all the peer connections the moment they are requested.
// As the stats will change over the life of a peer connection, this ensures that
// the updated values are returned.
func (tc *TrackerClient) RtcPeerConnStats() map[string]webrtc.StatsReport {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	sr := make(map[string]webrtc.StatsReport)
	for id, pc := range tc.rtcPeerConns {
		sr[id] = GetPeerConnStats(pc)
	}
	return sr
}

func (tc *TrackerClient) writeMessage(data []byte) error {
	for tc.wsConn == nil {
		if tc.closed {
			return fmt.Errorf("%T closed", tc)
		}
		tc.cond.Wait()
	}
	return tc.wsConn.WriteMessage(websocket.TextMessage, data)
}

func (tc *TrackerClient) trackerReadLoop(tracker *websocket.Conn) error {
	for {
		_, message, err := tracker.ReadMessage()
		if err != nil {
			return fmt.Errorf("read message error: %w", err)
		}
		tc.Logger.Levelf(log.Debug, "received message: %q", message)

		var ar AnnounceResponse
		if err := json.Unmarshal(message, &ar); err != nil {
			tc.Logger.WithDefaultLevel(log.Warning).Printf("error unmarshalling announce response: %v", err)
			continue
		}
		switch {
		case ar.Offer != nil:
			ih, err := jsonStringToInfoHash(ar.InfoHash)
			if err != nil {
				tc.Logger.WithDefaultLevel(log.Warning).Printf("error decoding info_hash in offer: %v", err)
				break
			}
			err = tc.handleOffer(offerContext{
				SessDesc: *ar.Offer,
				Id:       ar.OfferID,
				InfoHash: ih,
			}, ar.PeerID)
			if err != nil {
				tc.Logger.Levelf(log.Error, "handling offer for infohash %x: %v", ih, err)
			}
		case ar.Answer != nil:
			tc.handleAnswer(ar.OfferID, *ar.Answer)
		default:
			// wss://tracker.openwebtorrent.com appears to respond to an initial announces without
			// an offer or answer. I think that's fine. Let's check it at least contains an
			// infohash.
			_, err := jsonStringToInfoHash(ar.InfoHash)
			if err != nil {
				tc.Logger.Levelf(log.Warning, "unexpected announce response %q", message)
			}
		}
	}
}

type offerContext struct {
	SessDesc webrtc.SessionDescription
	Id       string
	InfoHash [20]byte
}

func (tc *TrackerClient) handleOffer(
	offerContext offerContext,
	peerId string,
) error {
	peerConnection, answer, err := tc.newAnsweringPeerConnection(offerContext)
	if err != nil {
		return fmt.Errorf("creating answering peer connection: %w", err)
	}

	// save the seeder peer connections
	tc.storePeerConnection(fmt.Sprintf("%x", offerContext.Id[:]), peerConnection)

	response := AnnounceResponse{
		Action:   "announce",
		InfoHash: binaryToJsonString(offerContext.InfoHash[:]),
		PeerID:   tc.peerIdBinary(),
		ToPeerID: peerId,
		Answer:   &answer,
		OfferID:  offerContext.Id,
	}
	data, err := json.Marshal(response)
	if err != nil {
		peerConnection.Close()
		return fmt.Errorf("marshalling response: %w", err)
	}
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if err := tc.writeMessage(data); err != nil {
		peerConnection.Close()
		return fmt.Errorf("writing response: %w", err)
	}
	return nil
}

func (tc *TrackerClient) handleAnswer(offerId string, answer webrtc.SessionDescription) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	offer, ok := tc.outboundOffers[offerId]
	if !ok {
		tc.Logger.WithDefaultLevel(log.Warning).Printf("could not find offer for id %+q", offerId)
		return
	}
	// tc.Logger.WithDefaultLevel(log.Debug).Printf("offer %q got answer %v", offerId, answer)
	metrics.Add("outbound offers answered", 1)
	err := offer.peerConnection.SetRemoteDescription(answer)
	if err != nil {
		err = fmt.Errorf("using outbound offer answer: %w", err)
		offer.peerConnection.span.RecordError(err)
		tc.Logger.LevelPrint(log.Error, err)
		return
	}
	delete(tc.outboundOffers, offerId)
	go tc.Announce(tracker.None, offer.infoHash)
}

func (tc *TrackerClient) storePeerConnection(offerId string, pc *wrappedPeerConnection) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if tc.rtcPeerConns == nil {
		tc.rtcPeerConns = make(map[string]*wrappedPeerConnection)
	}
	tc.rtcPeerConns[offerId] = pc
}
