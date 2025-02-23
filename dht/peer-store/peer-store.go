package peer_store

import (
	"github.com/anacrolix/torrent/metainfo"

	"github.com/james-lawrence/torrent/dht/krpc"
)

type InfoHash = metainfo.Hash

type Interface interface {
	AddPeer(InfoHash, krpc.NodeAddr)
	GetPeers(InfoHash) []krpc.NodeAddr
}
