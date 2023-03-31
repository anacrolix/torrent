package udp

import (
	"encoding"
	"fmt"

	"github.com/anacrolix/dht/v2/krpc"
)

// Marshalled as binary by the UDP client, so be careful making changes.
type AnnounceRequest struct {
	InfoHash   [20]byte
	PeerId     [20]byte
	Downloaded int64
	Left       int64 // If less than 0, math.MaxInt64 will be used for HTTP trackers instead.
	Uploaded   int64
	// Apparently this is optional. None can be used for announces done at
	// regular intervals.
	Event     AnnounceEvent
	IPAddress uint32
	Key       int32
	NumWant   int32 // How many peer addresses are desired. -1 for default.
	Port      uint16
} // 82 bytes

type AnnounceEvent int32

func (me *AnnounceEvent) UnmarshalText(text []byte) error {
	for key, str := range announceEventStrings {
		if string(text) == str {
			*me = AnnounceEvent(key)
			return nil
		}
	}
	return fmt.Errorf("unknown event")
}

var announceEventStrings = []string{"", "completed", "started", "stopped"}

func (e AnnounceEvent) String() string {
	// See BEP 3, "event", and
	// https://github.com/anacrolix/torrent/issues/416#issuecomment-751427001. Return a safe default
	// in case event values are not sanitized.
	if e < 0 || int(e) >= len(announceEventStrings) {
		return ""
	}
	return announceEventStrings[e]
}

type AnnounceResponsePeers interface {
	encoding.BinaryUnmarshaler
	NodeAddrs() []krpc.NodeAddr
}
