package torrent

import (
	pp "github.com/anacrolix/torrent/peer_protocol"
)

// These are called synchronously, and do not pass ownership. The Client and other locks may still
// be held. nil functions are not called.
type Callbacks struct {
	CompletedHandshake    func(_ *PeerConn, infoHash InfoHash)
	ReadMessage           func(*PeerConn, *pp.Message)
	ReadExtendedHandshake func(*PeerConn, *pp.ExtendedHandshakeMessage)
}
