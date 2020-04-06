package webtorrent

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/anacrolix/log"

	"github.com/anacrolix/torrent/tracker"
	"github.com/anacrolix/torrent/webtorrent/buffer"
	"github.com/gorilla/websocket"
	"github.com/pion/datachannel"
	"github.com/pion/webrtc/v2"
)

const (
	trackerURL = `wss://tracker.openwebtorrent.com/` // For simplicity
)

// Client represents the webtorrent client
type Client struct {
	lock           sync.Mutex
	peerIDBinary   string
	infoHashBinary string
	offeredPeers   map[string]Peer // OfferID to Peer
	tracker        *websocket.Conn
}

// Peer represents a remote peer
type Peer struct {
	peerID    string
	transport *Transport
}

func binaryToJsonString(b []byte) string {
	var seq []rune
	for _, v := range b {
		seq = append(seq, rune(v))
	}
	return string(seq)
}

func NewClient(peerId, infoHash [20]byte) *Client {
	return &Client{
		offeredPeers:   make(map[string]Peer),
		peerIDBinary:   binaryToJsonString(peerId[:]),
		infoHashBinary: binaryToJsonString(infoHash[:]),
	}
}

func (c *Client) Run(ar tracker.AnnounceRequest) error {
	t, _, err := websocket.DefaultDialer.Dial(trackerURL, nil)
	if err != nil {
		return fmt.Errorf("failed to dial tracker: %v", err)
	}
	defer t.Close()
	c.tracker = t

	go c.announce(ar)
	c.trackerReadLoop()

	return nil
}

func (c *Client) announce(request tracker.AnnounceRequest) error {
	transpot, offer, err := NewTransport()
	if err != nil {
		return fmt.Errorf("failed to create transport: %w", err)
	}

	randOfferID, err := buffer.RandomBytes(20)
	if err != nil {
		return fmt.Errorf("failed to generate bytes: %w", err)
	}
	// OfferID := randOfferID.ToStringHex()
	offerIDBinary := randOfferID.ToStringLatin1()

	c.lock.Lock()
	c.offeredPeers[offerIDBinary] = Peer{transport: transpot}
	c.lock.Unlock()

	req := AnnounceRequest{
		Numwant:    1, // If higher we need to create equal amount of offers
		Uploaded:   0,
		Downloaded: 0,
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
		return fmt.Errorf("failed to marshal request: %w", err)
	}
	c.lock.Lock()
	tracker := c.tracker
	err = tracker.WriteMessage(websocket.TextMessage, data)
	if err != nil {
		return fmt.Errorf("write AnnounceRequest: %w", err)
		c.lock.Unlock()
	}
	c.lock.Unlock()
	return nil
}

func (c *Client) trackerReadLoop() error {

	c.lock.Lock()
	tracker := c.tracker
	c.lock.Unlock()
	for {
		_, message, err := tracker.ReadMessage()
		if err != nil {
			return fmt.Errorf("read error: %w", err)
		}
		log.Printf("recv: %q", message)

		var ar AnnounceResponse
		if err := json.Unmarshal(message, &ar); err != nil {
			log.Printf("error unmarshaling announce response: %v", err)
			continue
		}
		if ar.InfoHash != c.infoHashBinary {
			log.Printf("announce response for different hash: expected %q got %q", c.infoHashBinary, ar.InfoHash)
			continue
		}
		switch {
		case ar.Offer != nil:
			t, answer, err := NewTransportFromOffer(*ar.Offer, c.handleDataChannel)
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

			// Do something with the peer
			_ = Peer{peerID: ar.PeerID, transport: t}
		case ar.Answer != nil:
			c.lock.Lock()
			peer, ok := c.offeredPeers[ar.OfferID]
			c.lock.Unlock()
			if !ok {
				log.Printf("could not find peer for offer %q", ar.OfferID)
				continue
			}
			log.Printf("offer %q got answer %q", ar.OfferID, ar.Answer)
			err = peer.transport.SetAnswer(*ar.Answer, c.handleDataChannel)
			if err != nil {
				return fmt.Errorf("failed to sent answer: %v", err)
			}
		}
	}
}

func (c *Client) handleDataChannel(dc datachannel.ReadWriteCloser) {
	go c.dcReadLoop(dc)
	//go c.dcWriteLoop(dc)
}

func (c *Client) dcReadLoop(d io.Reader) {
	for {
		buffer := make([]byte, 1024)
		n, err := d.Read(buffer)
		if err != nil {
			log.Printf("Datachannel closed; Exit the readloop: %v", err)
		}

		fmt.Printf("Message from DataChannel: %s\n", string(buffer[:n]))
	}
}

type AnnounceRequest struct {
	Numwant    int     `json:"numwant"`
	Uploaded   int     `json:"uploaded"`
	Downloaded int     `json:"downloaded"`
	Left       int64   `json:"left"`
	Event      string  `json:"event"`
	Action     string  `json:"action"`
	InfoHash   string  `json:"info_hash"`
	PeerID     string  `json:"peer_id"`
	Offers     []Offer `json:"offers"`
}

type Offer struct {
	OfferID string                    `json:"offer_id"`
	Offer   webrtc.SessionDescription `json:"offer"`
}

type AnnounceResponse struct {
	InfoHash   string                     `json:"info_hash"`
	Action     string                     `json:"action"`
	Interval   *int                       `json:"interval,omitempty"`
	Complete   *int                       `json:"complete,omitempty"`
	Incomplete *int                       `json:"incomplete,omitempty"`
	PeerID     string                     `json:"peer_id,omitempty"`
	ToPeerID   string                     `json:"to_peer_id,omitempty"`
	Answer     *webrtc.SessionDescription `json:"answer,omitempty"`
	Offer      *webrtc.SessionDescription `json:"offer,omitempty"`
	OfferID    string                     `json:"offer_id,omitempty"`
}
