package torrent

import "github.com/anacrolix/dht/krpc"

type peerExchangeMessage struct {
	Added       krpc.CompactIPv4NodeAddrs `bencode:"added"`
	AddedFlags  []pexPeerFlags            `bencode:"added.f"`
	Added6      krpc.CompactIPv6NodeAddrs `bencode:"added6"`
	AddedFlags6 []pexPeerFlags            `bencode:"added6.f"`
	Dropped     krpc.CompactIPv4NodeAddrs `bencode:"dropped"`
	Dropped6    krpc.CompactIPv6NodeAddrs `bencode:"dropped6"`
}

type pexPeerFlags byte

const (
	pexPrefersEncryption = 0x01
	pexSeedUploadOnly    = 0x02
	pexSupportsUtp       = 0x04
	pexHolepunchSupport  = 0x08
	pexOutgoingConn      = 0x10
)
