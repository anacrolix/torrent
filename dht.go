package torrent

import (
	"io"
	"net"

	"github.com/anacrolix/dht/v2"
	"github.com/anacrolix/dht/v2/krpc"
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

type DhtAnnounce interface {
	Close()
	Peers() <-chan dht.PeersValues
}

type anacrolixDhtServerWrapper struct {
	*dht.Server
}

func (me anacrolixDhtServerWrapper) Stats() interface{} {
	return me.Server.Stats()
}

type anacrolixDhtAnnounceWrapper struct {
	*dht.Announce
}

func (me anacrolixDhtAnnounceWrapper) Peers() <-chan dht.PeersValues {
	return me.Announce.Peers
}

func (me anacrolixDhtServerWrapper) Announce(hash [20]byte, port int, impliedPort bool) (DhtAnnounce, error) {
	ann, err := me.Server.Announce(hash, port, impliedPort)
	return anacrolixDhtAnnounceWrapper{ann}, err
}

func (me anacrolixDhtServerWrapper) Ping(addr *net.UDPAddr) {
	me.Server.Ping(addr, nil)
}

var _ DhtServer = anacrolixDhtServerWrapper{}
