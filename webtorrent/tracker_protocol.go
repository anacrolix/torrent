package webtorrent

import (
	"fmt"
	"math"

	"github.com/pion/webrtc/v2"
)

type AnnounceRequest struct {
	Numwant    int     `json:"numwant"`
	Uploaded   int64   `json:"uploaded"`
	Downloaded int64   `json:"downloaded"`
	Left       int64   `json:"left"`
	Event      string  `json:"event,omitempty"`
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

// I wonder if this is a defacto standard way to decode bytes to JSON for webtorrent. I don't really
// care.
func binaryToJsonString(b []byte) string {
	var seq []rune
	for _, v := range b {
		seq = append(seq, rune(v))
	}
	return string(seq)
}

func jsonStringToInfoHash(s string) (ih [20]byte, err error) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		panic(fmt.Sprintf("%q", s))
	}()
	for i, c := range []rune(s) {
		if c < 0 || c > math.MaxUint8 {
			err = fmt.Errorf("bad infohash string: %v", s)
			return
		}
		ih[i] = byte(c)
	}
	return
}
