package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sync"

	"github.com/anacrolix/torrent/metainfo"
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
	peerID       string
	peerIDBinary string

	infoHash       string
	infoHashBinary string
	totalLength    int

	offeredPeers map[string]Peer // OfferID to Peer

	tracker *websocket.Conn

	lock *sync.Mutex
}

// Peer represents a remote peer
type Peer struct {
	peerID    string
	transport *Transport
}

func NewClient() (*Client, error) {
	c := &Client{
		offeredPeers: make(map[string]Peer),
		lock:         &sync.Mutex{},
	}

	randPeerID, err := buffer.RandomBytes(9)
	if err != nil {
		return nil, fmt.Errorf("failed to generate bytes: %v", err)
	}
	peerIDBuffer := buffer.From("-WW0007-" + randPeerID.ToStringBase64())
	c.peerID = peerIDBuffer.ToStringHex()
	c.peerIDBinary = peerIDBuffer.ToStringLatin1()

	return c, nil
}

func (c *Client) LoadFile(p string) error {
	meta, err := metainfo.LoadFromFile(p)
	if err != nil {
		return fmt.Errorf("failed to load meta info: %v\n", err)
	}

	info, err := meta.UnmarshalInfo()
	if err != nil {
		return fmt.Errorf("failed to unmarshal info: %v\n", err)
	}
	c.totalLength = int(info.TotalLength())

	c.infoHash = meta.HashInfoBytes().String()
	b, err := buffer.FromHex(c.infoHash)
	if err != nil {
		return fmt.Errorf("failed to create buffer: %v\n", err)
	}
	c.infoHashBinary = b.ToStringLatin1()

	return nil
}

func (c *Client) Run() error {
	t, _, err := websocket.DefaultDialer.Dial(trackerURL, nil)
	if err != nil {
		return fmt.Errorf("failed to dial tracker: %v", err)
	}
	defer t.Close()
	c.tracker = t

	go c.announce()
	c.trackerReadLoop()

	return nil
}

func (c *Client) announce() {
	transpot, offer, err := NewTransport()
	if err != nil {
		log.Fatalf("failed to create transport: %v\n", err)
	}

	randOfferID, err := buffer.RandomBytes(20)
	if err != nil {
		log.Fatalf("failed to generate bytes: %v\n", err)
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
		Left:       int(c.totalLength),
		Event:      "started",
		Action:     "announce",
		InfoHash:   c.infoHashBinary,
		PeerID:     c.peerIDBinary,
		Offers: []Offer{
			{
				OfferID: offerIDBinary,
				Offer:   offer,
			}},
	}

	data, err := json.Marshal(req)
	if err != nil {
		log.Fatal("failed to marshal request:", err)
	}
	c.lock.Lock()
	tracker := c.tracker
	err = tracker.WriteMessage(websocket.TextMessage, data)
	if err != nil {
		log.Fatal("write AnnounceRequest:", err)
		c.lock.Unlock()
	}
	c.lock.Unlock()
}

func (c *Client) trackerReadLoop() {

	c.lock.Lock()
	tracker := c.tracker
	c.lock.Unlock()
	for {
		_, message, err := tracker.ReadMessage()
		if err != nil {
			log.Fatal("read error: %v", err)
		}
		log.Printf("recv: %s", message)

		var ar AnnounceResponse
		if err := json.Unmarshal(message, &ar); err != nil {
			log.Printf("error unmarshaling announce response: %v", err)
			continue
		}
		if ar.InfoHash != c.infoHashBinary {
			log.Printf("announce response for different hash: %s", ar.InfoHash)
			continue
		}
		switch {
		case ar.Offer != nil:
			t, answer, err := NewTransportFromOffer(*ar.Offer, c.handleDataChannel)
			if err != nil {
				log.Fatal("write AnnounceResponse:", err)
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
				log.Fatal("failed to marshal request:", err)
			}

			c.lock.Lock()
			err = tracker.WriteMessage(websocket.TextMessage, data)
			if err != nil {
				log.Fatal("write AnnounceResponse:", err)
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
				fmt.Printf("could not find peer for offer %s", ar.OfferID)
				continue
			}
			err = peer.transport.SetAnswer(*ar.Answer, c.handleDataChannel)
			if err != nil {
				log.Fatal("failed to sent answer: %v", err)
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
			log.Fatal("Datachannel closed; Exit the readloop:", err)
		}

		fmt.Printf("Message from DataChannel: %s\n", string(buffer[:n]))
	}
}

type AnnounceRequest struct {
	Numwant    int     `json:"numwant"`
	Uploaded   int     `json:"uploaded"`
	Downloaded int     `json:"downloaded"`
	Left       int     `json:"left"`
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
