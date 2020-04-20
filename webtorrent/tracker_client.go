package webtorrent

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/anacrolix/log"

	"github.com/anacrolix/torrent/tracker"
	"github.com/gorilla/websocket"
	"github.com/pion/datachannel"
	"github.com/pion/webrtc/v2"
)

// Client represents the webtorrent client
type TrackerClient struct {
	lock           sync.Mutex
	peerIDBinary   string
	infoHashBinary string
	outboundOffers map[string]outboundOffer // OfferID to outboundOffer
	tracker        *websocket.Conn
	onConn         onDataChannelOpen
	logger         log.Logger
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

func NewTrackerClient(peerId, infoHash [20]byte, onConn onDataChannelOpen, logger log.Logger) *TrackerClient {
	return &TrackerClient{
		outboundOffers: make(map[string]outboundOffer),
		peerIDBinary:   binaryToJsonString(peerId[:]),
		infoHashBinary: binaryToJsonString(infoHash[:]),
		onConn:         onConn,
		logger:         logger,
	}
}

func (c *TrackerClient) Run(ar tracker.AnnounceRequest, url string) error {
	t, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return fmt.Errorf("failed to dial tracker: %w", err)
	}
	defer t.Close()
	c.logger.WithDefaultLevel(log.Debug).Printf("dialed tracker %q", url)
	c.tracker = t

	go func() {
		err := c.announce(ar)
		if err != nil {
			c.logger.WithDefaultLevel(log.Error).Printf("error announcing: %v", err)
		}
	}()
	err = c.trackerReadLoop(c.tracker)
	c.lock.Lock()
	c.lock.Unlock()
	c.closeUnusedOffers()
	return err
}

func (c *TrackerClient) closeUnusedOffers() {
	for _, offer := range c.outboundOffers {
		if offer.answered {
			continue
		}
		offer.peerConnection.Close()
	}
}

func (c *TrackerClient) announce(request tracker.AnnounceRequest) error {
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

	req := AnnounceRequest{
		Numwant:    1, // If higher we need to create equal amount of offers.
		Uploaded:   request.Uploaded,
		Downloaded: request.Downloaded,
		Left:       request.Left,
		Event:      "started",
		Action:     "announce",
		InfoHash:   c.infoHashBinary,
		PeerID:     c.peerIDBinary,
		Offers: []Offer{{
			OfferID: offerIDBinary,
			Offer:   offer,
		}},
	}

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshalling request: %w", err)
	}

	c.lock.Lock()
	defer c.lock.Unlock()

	err = c.tracker.WriteMessage(websocket.TextMessage, data)
	if err != nil {
		pc.Close()
		return fmt.Errorf("write AnnounceRequest: %w", err)
	}

	c.outboundOffers[offerIDBinary] = outboundOffer{
		peerConnection: pc,
		dataChannel:    dc,
		originalOffer:  offer,
	}
	return nil
}

func (c *TrackerClient) trackerReadLoop(tracker *websocket.Conn) error {
	for {
		_, message, err := tracker.ReadMessage()
		if err != nil {
			return fmt.Errorf("read message error: %w", err)
		}
		c.logger.WithDefaultLevel(log.Debug).Printf("received message from tracker: %q", message)

		var ar AnnounceResponse
		if err := json.Unmarshal(message, &ar); err != nil {
			c.logger.WithDefaultLevel(log.Warning).Printf("error unmarshalling announce response: %v", err)
			continue
		}
		if ar.InfoHash != c.infoHashBinary {
			c.logger.Printf("announce response for different hash: expected %q got %q", c.infoHashBinary, ar.InfoHash)
			continue
		}
		switch {
		case ar.Offer != nil:
			answer, err := getAnswerForOffer(*ar.Offer, c.onConn, ar.OfferID)
			if err != nil {
				return fmt.Errorf("write AnnounceResponse: %w", err)
			}

			req := AnnounceResponse{
				Action:   "announce",
				InfoHash: c.infoHashBinary,
				PeerID:   c.peerIDBinary,
				ToPeerID: ar.PeerID,
				Answer:   &answer,
				OfferID:  ar.OfferID,
			}
			data, err := json.Marshal(req)
			if err != nil {
				return fmt.Errorf("failed to marshal request: %w", err)
			}

			c.lock.Lock()
			err = tracker.WriteMessage(websocket.TextMessage, data)
			if err != nil {
				return fmt.Errorf("write AnnounceResponse: %w", err)
				c.lock.Unlock()
			}
			c.lock.Unlock()
		case ar.Answer != nil:
			c.lock.Lock()
			offer, ok := c.outboundOffers[ar.OfferID]
			c.lock.Unlock()
			if !ok {
				c.logger.WithDefaultLevel(log.Warning).Printf("could not find offer for id %q", ar.OfferID)
				continue
			}
			c.logger.Printf("offer %q got answer %v", ar.OfferID, *ar.Answer)
			err = offer.setAnswer(*ar.Answer, func(dc datachannel.ReadWriteCloser) {
				c.onConn(dc, DataChannelContext{
					Local:        offer.originalOffer,
					Remote:       *ar.Answer,
					OfferId:      ar.OfferID,
					LocalOffered: true,
				})
			})
			if err != nil {
				return fmt.Errorf("failed to sent answer: %w", err)
			}
			offer.answered = true
		}
	}
}
