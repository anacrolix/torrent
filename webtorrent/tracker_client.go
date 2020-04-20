package webtorrent

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/anacrolix/log"

	"github.com/anacrolix/torrent/tracker"
	"github.com/gorilla/websocket"
	"github.com/pion/datachannel"
	"github.com/pion/webrtc/v2"
)

// Client represents the webtorrent client
type TrackerClient struct {
	Url                string
	GetAnnounceRequest func(tracker.AnnounceEvent) tracker.AnnounceRequest
	PeerId             [20]byte
	InfoHash           [20]byte
	OnConn             onDataChannelOpen
	Logger             log.Logger

	lock           sync.Mutex
	outboundOffers map[string]outboundOffer // OfferID to outboundOffer
	wsConn         *websocket.Conn
}

func (me *TrackerClient) peerIdBinary() string {
	return binaryToJsonString(me.PeerId[:])
}

func (me *TrackerClient) infoHashBinary() string {
	return binaryToJsonString(me.InfoHash[:])
}

// outboundOffer represents an outstanding offer.
type outboundOffer struct {
	originalOffer  webrtc.SessionDescription
	peerConnection wrappedPeerConnection
	dataChannel    *webrtc.DataChannel
	// Whether we've received an answer for this offer, and closing its PeerConnection has been
	// handed off.
	answered bool
}

type DataChannelContext struct {
	Local, Remote webrtc.SessionDescription
	OfferId       string
	LocalOffered  bool
}

type onDataChannelOpen func(_ datachannel.ReadWriteCloser, dcc DataChannelContext)

func (tc *TrackerClient) doWebsocket() error {
	c, _, err := websocket.DefaultDialer.Dial(tc.Url, nil)
	if err != nil {
		return fmt.Errorf("dialing tracker: %w", err)
	}
	defer c.Close()
	tc.Logger.WithDefaultLevel(log.Debug).Printf("dialed tracker %q", tc.Url)
	tc.wsConn = c
	go func() {
		err := tc.announce(tracker.Started)
		if err != nil {
			tc.Logger.WithDefaultLevel(log.Error).Printf("error in initial announce: %v", err)
		}
	}()
	err = tc.trackerReadLoop(tc.wsConn)
	tc.lock.Lock()
	tc.closeUnusedOffers()
	tc.lock.Unlock()
	return err
}

func (tc *TrackerClient) Run() error {
	for {
		err := tc.doWebsocket()
		tc.Logger.Printf("websocket instance ended: %v", err)
		time.Sleep(time.Minute)
	}
}

func (tc *TrackerClient) closeUnusedOffers() {
	for _, offer := range tc.outboundOffers {
		if offer.answered {
			continue
		}
		offer.peerConnection.Close()
	}
	tc.outboundOffers = nil
}

func (tc *TrackerClient) announce(event tracker.AnnounceEvent) error {
	var randOfferId [20]byte
	_, err := rand.Read(randOfferId[:])
	if err != nil {
		return fmt.Errorf("generating offer_id bytes: %w", err)
	}
	offerIDBinary := binaryToJsonString(randOfferId[:])

	pc, dc, offer, err := newOffer()
	if err != nil {
		return fmt.Errorf("creating offer: %w", err)
	}

	request := tc.GetAnnounceRequest(event)

	req := AnnounceRequest{
		Numwant:    1, // If higher we need to create equal amount of offers.
		Uploaded:   request.Uploaded,
		Downloaded: request.Downloaded,
		Left:       request.Left,
		Event:      request.Event.String(),
		Action:     "announce",
		InfoHash:   tc.infoHashBinary(),
		PeerID:     tc.peerIdBinary(),
		Offers: []Offer{{
			OfferID: offerIDBinary,
			Offer:   offer,
		}},
	}

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshalling request: %w", err)
	}

	tc.lock.Lock()
	defer tc.lock.Unlock()

	err = tc.wsConn.WriteMessage(websocket.TextMessage, data)
	if err != nil {
		pc.Close()
		return fmt.Errorf("write AnnounceRequest: %w", err)
	}
	if tc.outboundOffers == nil {
		tc.outboundOffers = make(map[string]outboundOffer)
	}
	tc.outboundOffers[offerIDBinary] = outboundOffer{
		peerConnection: pc,
		dataChannel:    dc,
		originalOffer:  offer,
	}
	return nil
}

func (tc *TrackerClient) trackerReadLoop(tracker *websocket.Conn) error {
	for {
		_, message, err := tracker.ReadMessage()
		if err != nil {
			return fmt.Errorf("read message error: %w", err)
		}
		tc.Logger.WithDefaultLevel(log.Debug).Printf("received message from tracker: %q", message)

		var ar AnnounceResponse
		if err := json.Unmarshal(message, &ar); err != nil {
			tc.Logger.WithDefaultLevel(log.Warning).Printf("error unmarshalling announce response: %v", err)
			continue
		}
		if ar.InfoHash != tc.infoHashBinary() {
			tc.Logger.Printf("announce response for different hash: expected %q got %q", tc.infoHashBinary(), ar.InfoHash)
			continue
		}
		switch {
		case ar.Offer != nil:
			answer, err := getAnswerForOffer(*ar.Offer, tc.OnConn, ar.OfferID)
			if err != nil {
				return fmt.Errorf("write AnnounceResponse: %w", err)
			}

			req := AnnounceResponse{
				Action:   "announce",
				InfoHash: tc.infoHashBinary(),
				PeerID:   tc.peerIdBinary(),
				ToPeerID: ar.PeerID,
				Answer:   &answer,
				OfferID:  ar.OfferID,
			}
			data, err := json.Marshal(req)
			if err != nil {
				return fmt.Errorf("failed to marshal request: %w", err)
			}

			tc.lock.Lock()
			err = tracker.WriteMessage(websocket.TextMessage, data)
			if err != nil {
				return fmt.Errorf("write AnnounceResponse: %w", err)
				tc.lock.Unlock()
			}
			tc.lock.Unlock()
		case ar.Answer != nil:
			tc.handleAnswer(ar.OfferID, *ar.Answer)
		}
	}
}

func (tc *TrackerClient) handleAnswer(offerId string, answer webrtc.SessionDescription) {
	tc.lock.Lock()
	defer tc.lock.Unlock()
	offer, ok := tc.outboundOffers[offerId]
	if !ok {
		tc.Logger.WithDefaultLevel(log.Warning).Printf("could not find offer for id %q", offerId)
		return
	}
	tc.Logger.Printf("offer %q got answer %v", offerId, answer)
	err := offer.setAnswer(answer, func(dc datachannel.ReadWriteCloser) {
		tc.OnConn(dc, DataChannelContext{
			Local:        offer.originalOffer,
			Remote:       answer,
			OfferId:      offerId,
			LocalOffered: true,
		})
	})
	if err != nil {
		tc.Logger.WithDefaultLevel(log.Warning).Printf("error using outbound offer answer: %v", err)
		return
	}
	delete(tc.outboundOffers, offerId)
	go tc.announce(tracker.None)
}
