package torrent

import (
	"net"

	"github.com/anacrolix/dht/v2/krpc"

	"github.com/anacrolix/torrent/peer_protocol"
)

// Peer connection info, handed about publicly.
type Peer struct {
	Id     [20]byte
	Addr   net.Addr
	Source PeerSource
	// Peer is known to support encryption.
	SupportsEncryption bool
	peer_protocol.PexPeerFlags
	// Whether we can ignore poor or bad behaviour from the peer.
	Trusted bool
}

// FromPex generate Peer from peer exchange
func (me *Peer) FromPex(na krpc.NodeAddr, fs peer_protocol.PexPeerFlags) {
	me.Addr = ipPortAddr{append([]byte(nil), na.IP...), na.Port}
	me.Source = PeerSourcePex
	// If they prefer encryption, they must support it.
	if fs.Get(peer_protocol.PexPrefersEncryption) {
		me.SupportsEncryption = true
	}
	me.PexPeerFlags = fs
}

func (me Peer) addr() IpPort {
	return IpPort{IP: addrIpOrNil(me.Addr), Port: uint16(addrPortOrZero(me.Addr))}
}
