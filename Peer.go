package torrent

import (
	"net"

	"github.com/james-lawrence/torrent/btprotocol"
	"github.com/james-lawrence/torrent/dht/v2/krpc"
)

// Peer connection info, handed about publicly.
type Peer struct {
	ID     [20]byte
	IP     net.IP
	Port   int
	Source peerSource
	// Peer is known to support encryption.
	SupportsEncryption bool
	btprotocol.PexPeerFlags
	// Whether we can ignore poor or bad behaviour from the peer.
	Trusted bool
}

// FromPex generate Peer from peer exchange
func (me *Peer) FromPex(na krpc.NodeAddr, fs btprotocol.PexPeerFlags) {
	me.IP = append([]byte(nil), na.IP...)
	me.Port = na.Port
	me.Source = peerSourcePex
	// If they prefer encryption, they must support it.
	if fs.Get(btprotocol.PexPrefersEncryption) {
		me.SupportsEncryption = true
	}
	me.PexPeerFlags = fs
}

func (me Peer) addr() IpPort {
	return IpPort{IP: me.IP, Port: uint16(me.Port)}
}
