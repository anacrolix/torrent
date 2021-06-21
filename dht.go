package torrent

import (
	"io"
	"net"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/dht/v2/krpc"
	peer_store "github.com/anacrolix/dht/v2/peer-store"
)

type DhtServer interface {
	Stats() interface{}
	ID() [20]byte
	Addr() net.Addr
	AddNode(ni krpc.NodeInfo) error
	Ping(addr *net.UDPAddr)
	Announce(hash [20]byte, port int, impliedPort bool) (DhtAnnounce, error)
	WriteStatus(io.Writer)
}

// Optional interface for DhtServer's that can expose their peer store (if any).
type PeerStorer interface {
	PeerStore() peer_store.Interface
}

type DhtAnnounce interface {
	Close()
	Peers() <-chan dht.PeersValues
}

type AnacrolixDhtServerWrapper struct {
	*dht.Server
}

func (me AnacrolixDhtServerWrapper) Stats() interface{} {
	return me.Server.Stats()
}

type anacrolixDhtAnnounceWrapper struct {
	*dht.Announce
}

func (me anacrolixDhtAnnounceWrapper) Peers() <-chan dht.PeersValues {
	return me.Announce.Peers
}

func (me AnacrolixDhtServerWrapper) Announce(hash [20]byte, port int, impliedPort bool) (DhtAnnounce, error) {
	ann, err := me.Server.Announce(hash, port, impliedPort)
	return anacrolixDhtAnnounceWrapper{ann}, err
}

func (me AnacrolixDhtServerWrapper) Ping(addr *net.UDPAddr) {
	me.Server.Ping(addr)
}

var _ DhtServer = AnacrolixDhtServerWrapper{}
